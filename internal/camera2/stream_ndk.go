//go:build android || hwcamera_typecheck

package camera2

import (
	"context"
	"sync"
	"time"
	"unsafe"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// frameQueue is how many frames wait for Read. A slow reader loses the
// oldest ones; Sequence keeps counting so the gap shows.
const frameQueue = 3

// defaultImages is the AImageReader depth. Images go back to the reader
// as soon as they are copied, so few are needed.
const defaultImages = 4

// streams maps the context handles given to the NDK callbacks to their
// streams, keeping Go pointers out of NDK memory.
var (
	streamsMu  sync.Mutex
	streams    = map[uintptr]*stream{}
	nextHandle uintptr
)

func register(s *stream) uintptr {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	nextHandle++
	streams[nextHandle] = s
	return nextHandle
}

func unregister(h uintptr) {
	streamsMu.Lock()
	delete(streams, h)
	streamsMu.Unlock()
}

func lookup(h uintptr) *stream {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	return streams[h]
}

type stream struct {
	handle uintptr
	mode   camera.Mode

	mgr, reader, window, device, container, output, target, request, session uintptr

	// The NDK copies these, but they stay referenced for the stream's
	// lifetime all the same.
	devCB    deviceCallbacks
	sessCB   sessionCallbacks
	listener imageListener

	frames    chan *camera.Frame
	done      chan struct{}
	closeOnce sync.Once

	failed   chan struct{}
	failOnce sync.Once
	failErr  error

	// cbMu serialises the image callback (an NDK looper thread) with
	// Close; the fields below it are touched only under it.
	cbMu    sync.Mutex
	closed  bool
	seq     uint64
	firstTS int64
	haveTS  bool
}

func openStream(mgr uintptr, id string, mode camera.Mode, fps float64, ranges []fpsRange, nbuf int) (_ *stream, err error) {
	if nbuf <= 0 {
		nbuf = defaultImages
	}
	nbuf = min(max(nbuf, 2), 16)
	s := &stream{
		mode:   mode,
		frames: make(chan *camera.Frame, frameQueue),
		done:   make(chan struct{}),
		failed: make(chan struct{}),
	}
	s.handle = register(s)
	defer func() {
		if err != nil {
			s.cbMu.Lock()
			s.closed = true
			s.cbMu.Unlock()
			s.teardown()
		}
	}()

	if st := AImageReader_new(int32(mode.Width), int32(mode.Height), formatYUV420888, int32(nbuf), &s.reader); st != mediaOK {
		return nil, &camera.BackendError{Backend: Name, Op: "AImageReader_new", Code: int64(st)}
	}
	s.listener = imageListener{Context: s.handle, OnImageAvailable: cbImage}
	if st := AImageReader_setImageListener(s.reader, &s.listener); st != mediaOK {
		return nil, &camera.BackendError{Backend: Name, Op: "AImageReader_setImageListener", Code: int64(st)}
	}
	if st := AImageReader_getWindow(s.reader, &s.window); st != mediaOK || s.window == 0 {
		return nil, &camera.BackendError{Backend: Name, Op: "AImageReader_getWindow", Code: int64(st)}
	}

	s.devCB = deviceCallbacks{Context: s.handle, OnDisconnected: cbDisconnected, OnError: cbError}
	if st := ACameraManager_openCamera(mgr, id, &s.devCB, &s.device); st != statusOK {
		return nil, statusErr("ACameraManager_openCamera", st)
	}

	if st := ACaptureSessionOutputContainer_create(&s.container); st != statusOK {
		return nil, statusErr("ACaptureSessionOutputContainer_create", st)
	}
	if st := ACaptureSessionOutput_create(s.window, &s.output); st != statusOK {
		return nil, statusErr("ACaptureSessionOutput_create", st)
	}
	if st := ACaptureSessionOutputContainer_add(s.container, s.output); st != statusOK {
		return nil, statusErr("ACaptureSessionOutputContainer_add", st)
	}
	if st := ACameraOutputTarget_create(s.window, &s.target); st != statusOK {
		return nil, statusErr("ACameraOutputTarget_create", st)
	}
	// TEMPLATE_RECORD favours a steady frame rate; some external cameras
	// only offer PREVIEW.
	if st := ACameraDevice_createCaptureRequest(s.device, templateRecord, &s.request); st != statusOK {
		if st2 := ACameraDevice_createCaptureRequest(s.device, templatePreview, &s.request); st2 != statusOK {
			return nil, statusErr("ACameraDevice_createCaptureRequest", st2)
		}
	}
	if st := ACaptureRequest_addTarget(s.request, s.target); st != statusOK {
		return nil, statusErr("ACaptureRequest_addTarget", st)
	}
	if fps > 0 {
		if r, ok := pickRange(ranges, fps); ok {
			v := [2]int32{r.Min, r.Max}
			if ACaptureRequest_setEntry_i32(s.request, tagControlAETargetFPSRange, 2, &v[0]) == statusOK {
				fps = float64(r.Max)
			}
		}
	}
	s.mode.FrameRates = nil
	if fps > 0 {
		s.mode.FrameRates = []float64{fps}
	}

	s.sessCB = sessionCallbacks{Context: s.handle, OnClosed: cbSessionNop, OnReady: cbSessionNop, OnActive: cbSessionNop}
	if st := ACameraDevice_createCaptureSession(s.device, s.container, &s.sessCB, &s.session); st != statusOK {
		return nil, statusErr("ACameraDevice_createCaptureSession", st)
	}
	req := s.request
	if st := ACameraCaptureSession_setRepeatingRequest(s.session, 0, 1, &req, nil); st != statusOK {
		return nil, statusErr("ACameraCaptureSession_setRepeatingRequest", st)
	}
	s.mgr = mgr
	return s, nil
}

func (s *stream) Mode() camera.Mode { return s.mode }

func (s *stream) Read(ctx context.Context) (*camera.Frame, error) {
	select {
	case <-s.done:
		return nil, camera.ErrClosed
	default:
	}
	select {
	case f := <-s.frames:
		return f, nil
	case <-s.done:
		return nil, camera.ErrClosed
	case <-s.failed:
		// Hand out what was captured before the failure first.
		select {
		case f := <-s.frames:
			return f, nil
		default:
		}
		return nil, s.failErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *stream) fail(err error) {
	s.failOnce.Do(func() {
		s.failErr = err
		close(s.failed)
	})
}

func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		// After this no callback touches the reader; one in flight
		// finishes first.
		s.cbMu.Lock()
		s.closed = true
		s.cbMu.Unlock()
		s.teardown()
	})
	return nil
}

// teardown releases what openStream created, in the order of the NDK
// samples; it copes with a partially built stream.
func (s *stream) teardown() {
	if s.session != 0 {
		ACameraCaptureSession_stopRepeating(s.session)
		ACameraCaptureSession_close(s.session)
		s.session = 0
	}
	if s.request != 0 {
		if s.target != 0 {
			ACaptureRequest_removeTarget(s.request, s.target)
		}
		ACaptureRequest_free(s.request)
		s.request = 0
	}
	if s.target != 0 {
		ACameraOutputTarget_free(s.target)
		s.target = 0
	}
	if s.output != 0 {
		if s.container != 0 {
			ACaptureSessionOutputContainer_remove(s.container, s.output)
		}
		ACaptureSessionOutput_free(s.output)
		s.output = 0
	}
	if s.container != 0 {
		ACaptureSessionOutputContainer_free(s.container)
		s.container = 0
	}
	if s.device != 0 {
		// Synchronous: no device callback runs after it returns.
		ACameraDevice_close(s.device)
		s.device = 0
	}
	if s.reader != 0 {
		AImageReader_setImageListener(s.reader, nil)
		// Deletes the window and returns every image to the reader.
		AImageReader_delete(s.reader)
		s.reader, s.window = 0, 0
	}
	unregister(s.handle)
	if s.mgr != 0 {
		ACameraManager_delete(s.mgr)
		s.mgr = 0
	}
}

// onImage runs on the AImageReader's callback thread. It takes every image
// waiting, keeps the newest, and copies it into a Go-owned NV12 frame; the
// images skipped count as dropped frames.
func (s *stream) onImage() {
	s.cbMu.Lock()
	defer s.cbMu.Unlock()
	if s.closed || s.reader == 0 {
		return
	}
	var last uintptr
	for {
		var img uintptr
		if AImageReader_acquireNextImage(s.reader, &img) != mediaOK || img == 0 {
			break
		}
		if last != 0 {
			AImage_delete(last)
			s.seq++
		}
		last = img
	}
	if last == 0 {
		return
	}
	f := s.copyImage(last)
	AImage_delete(last)
	seq := s.seq
	s.seq++
	if f == nil {
		return
	}
	f.Sequence = seq
	for {
		select {
		case s.frames <- f:
			return
		default:
		}
		select {
		case <-s.frames: // drop the oldest
		default:
		}
	}
}

func planeOf(img uintptr, i int32) (p plane, base unsafe.Pointer, ok bool) {
	var data *byte
	var n, rs, ps int32
	if AImage_getPlaneData(img, i, &data, &n) != mediaOK || data == nil || n <= 0 ||
		AImage_getPlaneRowStride(img, i, &rs) != mediaOK ||
		AImage_getPlanePixelStride(img, i, &ps) != mediaOK {
		return plane{}, nil, false
	}
	return plane{Data: unsafe.Slice(data, n), RowStride: int(rs), PixelStride: int(ps)}, unsafe.Pointer(data), true
}

func (s *stream) copyImage(img uintptr) *camera.Frame {
	var w, h int32
	if AImage_getWidth(img, &w) != mediaOK || AImage_getHeight(img, &h) != mediaOK || w < 2 || h < 2 {
		return nil
	}
	var cr cropRect
	cropOK := AImage_getCropRect(img, &cr) == mediaOK
	x0, y0, cw, ch := cropWindow(int(w), int(h), int(cr.Left), int(cr.Top), int(cr.Right), int(cr.Bottom), cropOK)

	y, _, ok1 := planeOf(img, 0)
	u, ub, ok2 := planeOf(img, 1)
	v, vb, ok3 := planeOf(img, 2)
	if !ok1 || !ok2 || !ok3 ||
		!y.covers(x0, y0, cw, ch) || !u.covers(x0/2, y0/2, cw/2, ch/2) || !v.covers(x0/2, y0/2, cw/2, ch/2) {
		return nil
	}
	inter := u.PixelStride == 2 && v.PixelStride == 2 && unsafe.Add(ub, 1) == vb
	buf := make([]byte, nv12Size(cw, ch))
	toNV12(buf, x0, y0, cw, ch, y, u, v, inter)

	var ts int64
	AImage_getTimestamp(img, &ts)
	if !s.haveTS {
		s.firstTS, s.haveTS = ts, true
	}
	return &camera.Frame{
		Width: cw, Height: ch, Format: camera.NV12,
		Planes:    [][]byte{buf[:cw*ch], buf[cw*ch:]},
		Strides:   []int{cw, cw},
		Timestamp: time.Duration(ts - s.firstTS),
	}
}

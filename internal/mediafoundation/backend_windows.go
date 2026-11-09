//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/mediafoundation/sys"
)

const videoStream = sys.MF_SOURCE_READER_FIRST_VIDEO_STREAM

// Available reports whether the Media Foundation DLLs load.
func (Backend) Available() bool { return sys.Load() == nil }

func loadErr() error {
	if err := sys.Load(); err != nil {
		return fmt.Errorf("%w: %v", camera.ErrUnsupported, err)
	}
	return nil
}

// onCOMThread runs fn on a new OS thread that has joined the multithreaded
// apartment and started Media Foundation, and waits for it or ctx. When ctx
// ends first fn keeps running in the background and its result is dropped.
func onCOMThread[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		// The thread is never unlocked: it exits with the goroutine, after
		// leaving COM, so no other goroutine inherits its apartment.
		runtime.LockOSThread()
		var r result
		uninit, err := sys.CoInitializeMTA()
		if err != nil {
			r.err = err
			ch <- r
			return
		}
		if uninit {
			defer sys.CoUninitialize()
		}
		if hr := sys.Startup(); hr.Failed() {
			r.err = hrError("MFStartup", hr)
			ch <- r
			return
		}
		defer sys.Shutdown()
		r.v, r.err = fn()
		ch <- r
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// enumDevices returns the activation objects of the video capture devices.
// The caller releases each.
func enumDevices() ([]*sys.IMFActivate, error) {
	var attrs *sys.IMFAttributes
	if hr := sys.MFCreateAttributes(&attrs, 1); hr.Failed() {
		return nil, hrError("MFCreateAttributes", hr)
	}
	defer attrs.Release()
	if hr := attrs.SetGUID(&sys.MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE, &sys.MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID); hr.Failed() {
		return nil, hrError("IMFAttributes::SetGUID(MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE)", hr)
	}
	var arr **sys.IMFActivate
	var n uint32
	if hr := sys.MFEnumDeviceSources(attrs, &arr, &n); hr.Failed() {
		return nil, hrError("MFEnumDeviceSources", hr)
	}
	if arr == nil {
		return nil, nil
	}
	list := make([]*sys.IMFActivate, n)
	copy(list, unsafe.Slice(arr, n))
	sys.CoTaskMemFree(unsafe.Pointer(arr))
	return list, nil
}

func releaseAll(list []*sys.IMFActivate) {
	for _, a := range list {
		a.Release()
	}
}

// describeType reads the format, size and rate of a native media type.
func describeType(mt *sys.IMFMediaType, index uint32) nativeType {
	a := mt.Attributes()
	t := nativeType{index: index}
	if g, ok := a.GUID(&sys.MF_MT_SUBTYPE); ok {
		t.format = formatFromSubtype(g)
	}
	if v, ok := a.UINT64(&sys.MF_MT_FRAME_SIZE); ok {
		w, h := unpack(v)
		t.width, t.height = int(w), int(h)
	}
	if v, ok := a.UINT64(&sys.MF_MT_FRAME_RATE); ok {
		t.rate = rateOf(unpack(v))
	}
	return t
}

// nativeTypes lists the formats the reader's first video stream offers.
func nativeTypes(r *sys.IMFSourceReader) ([]nativeType, error) {
	var types []nativeType
	for i := uint32(0); i < 4096; i++ {
		var mt *sys.IMFMediaType
		hr := r.GetNativeMediaType(videoStream, i, &mt)
		if hr == sys.MF_E_NO_MORE_TYPES {
			break
		}
		if hr.Failed() {
			if i == 0 {
				return nil, hrError("IMFSourceReader::GetNativeMediaType", hr)
			}
			break
		}
		types = append(types, describeType(mt, i))
		mt.Release()
	}
	return types, nil
}

// probeModes activates a device just long enough to list its native types,
// then shuts it down so the camera does not stay on.
func probeModes(act *sys.IMFActivate) ([]camera.Mode, error) {
	var src *sys.IMFMediaSource
	if hr := act.ActivateObject(&sys.IID_IMFMediaSource, unsafe.Pointer(&src)); hr.Failed() {
		return nil, hrError("IMFActivate::ActivateObject", hr)
	}
	defer func() {
		src.Shutdown()
		src.Release()
		act.ShutdownObject()
	}()
	var reader *sys.IMFSourceReader
	if hr := sys.MFCreateSourceReaderFromMediaSource(src, nil, &reader); hr.Failed() {
		return nil, hrError("MFCreateSourceReaderFromMediaSource", hr)
	}
	defer reader.Release()
	types, err := nativeTypes(reader)
	if err != nil {
		return nil, err
	}
	return mergeModes(types), nil
}

// Devices lists the video capture devices with their modes. A device whose
// modes cannot be read (in use exclusively, access denied) is listed
// without modes; Open on it reports the reason.
func (Backend) Devices(ctx context.Context) ([]camera.DeviceInfo, error) {
	if err := loadErr(); err != nil {
		return nil, err
	}
	return onCOMThread(ctx, func() ([]camera.DeviceInfo, error) {
		acts, err := enumDevices()
		if err != nil {
			return nil, err
		}
		defer releaseAll(acts)
		var out []camera.DeviceInfo
		for _, a := range acts {
			id, ok := a.Attributes().String(&sys.MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK)
			if !ok || id == "" {
				continue
			}
			name, _ := a.Attributes().String(&sys.MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME)
			modes, _ := probeModes(a)
			out = append(out, camera.DeviceInfo{
				ID:       id,
				Name:     name,
				Backend:  Name,
				Position: positionFor(id),
				Modes:    modes,
			})
		}
		return out, nil
	})
}

type readResult struct {
	f   *camera.Frame
	err error
}

// stream is an open camera. Every COM call on the reader happens on the
// worker goroutine, which is locked to its OS thread; Read talks to it
// through channels so that it can give up on a context.
type stream struct {
	mode camera.Mode

	reqs    chan struct{}   // one ReadSample per request
	results chan readResult // at most one outstanding result
	done    chan struct{}   // closed by Close
	exited  chan struct{}   // closed when the worker has released everything
	sem     chan struct{}   // serializes Read
	pending bool            // a request is outstanding (guarded by sem)

	closeOnce sync.Once

	// Owned by the worker; srcMu also lets Close shut the source down from
	// another thread when the worker is stuck in ReadSample.
	srcMu  sync.Mutex
	act    *sys.IMFActivate
	source *sys.IMFMediaSource
	reader *sys.IMFSourceReader

	layout layout
	clock  clock
	fatal  error
}

// Open opens the camera whose symbolic link is id, configures the mode
// closest to cfg and starts streaming. The first frame is read before
// Open returns so that a busy or blocked camera fails here.
func (Backend) Open(ctx context.Context, id string, cfg camera.Config) (camera.Stream, error) {
	if err := loadErr(); err != nil {
		return nil, err
	}
	s := &stream{
		reqs:    make(chan struct{}),
		results: make(chan readResult, 1),
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
		sem:     make(chan struct{}, 1),
	}
	ready := make(chan error, 1)
	go s.run(id, cfg, ready)
	select {
	case err := <-ready:
		if err != nil {
			<-s.exited
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		go s.Close()
		return nil, ctx.Err()
	}
}

func (s *stream) run(id string, cfg camera.Config, ready chan<- error) {
	runtime.LockOSThread() // never unlocked; the thread exits with the goroutine
	defer close(s.exited)
	uninit, err := sys.CoInitializeMTA()
	if err != nil {
		ready <- err
		return
	}
	if uninit {
		defer sys.CoUninitialize()
	}
	if hr := sys.Startup(); hr.Failed() {
		ready <- hrError("MFStartup", hr)
		return
	}
	defer sys.Shutdown()
	defer s.release()

	if err := s.open(id, cfg); err != nil {
		ready <- err
		return
	}
	first := s.readOne()
	if first.err != nil {
		ready <- first.err
		return
	}
	s.results <- first
	s.pending = true
	ready <- nil

	for {
		select {
		case <-s.done:
			return
		case <-s.reqs:
		}
		var r readResult
		if s.fatal != nil {
			r.err = s.fatal
		} else if r = s.readOne(); r.err != nil {
			s.fatal = r.err
		}
		s.results <- r
	}
}

// release frees every COM object; it runs on the worker thread.
func (s *stream) release() {
	s.srcMu.Lock()
	defer s.srcMu.Unlock()
	// Releasing the reader also shuts the source down (unless
	// MF_SOURCE_READER_DISCONNECT_MEDIASOURCE_ON_SHUTDOWN is set);
	// shutting it down again is harmless and covers the case where the
	// reader was never created.
	s.reader.Release()
	s.reader = nil
	if s.source != nil {
		s.source.Shutdown()
		s.source.Release()
		s.source = nil
	}
	if s.act != nil {
		s.act.ShutdownObject()
		s.act.Release()
		s.act = nil
	}
}

func (s *stream) open(id string, cfg camera.Config) error {
	acts, err := enumDevices()
	if err != nil {
		return err
	}
	for _, a := range acts {
		if l, ok := a.Attributes().String(&sys.MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK); ok && strings.EqualFold(l, id) {
			a.Attributes().Unknown().AddRef()
			s.act = a
			break
		}
	}
	releaseAll(acts)
	if s.act == nil {
		return fmt.Errorf("%w: %s", camera.ErrNotFound, id)
	}

	var src *sys.IMFMediaSource
	if hr := s.act.ActivateObject(&sys.IID_IMFMediaSource, unsafe.Pointer(&src)); hr.Failed() {
		return hrError("IMFActivate::ActivateObject", hr)
	}
	s.srcMu.Lock()
	s.source = src
	s.srcMu.Unlock()

	var attrs *sys.IMFAttributes
	if hr := sys.MFCreateAttributes(&attrs, 2); hr.Failed() {
		return hrError("MFCreateAttributes", hr)
	}
	// The advanced video processor lets the reader decode MJPEG and convert
	// between YUV layouts (Windows 8+).
	attrs.SetUINT32(&sys.MF_SOURCE_READER_ENABLE_ADVANCED_VIDEO_PROCESSING, 1)
	attrs.SetUINT32(&sys.MF_READWRITE_DISABLE_CONVERTERS, 0)
	var reader *sys.IMFSourceReader
	hr := sys.MFCreateSourceReaderFromMediaSource(src, attrs, &reader)
	attrs.Release()
	if hr.Failed() {
		return hrError("MFCreateSourceReaderFromMediaSource", hr)
	}
	s.srcMu.Lock()
	s.reader = reader
	s.srcMu.Unlock()

	reader.SetStreamSelection(sys.MF_SOURCE_READER_ALL_STREAMS, false)
	if hr := reader.SetStreamSelection(videoStream, true); hr.Failed() {
		return hrError("IMFSourceReader::SetStreamSelection", hr)
	}

	types, err := nativeTypes(reader)
	if err != nil {
		return err
	}
	m, err := camera.ChooseMode(mergeModes(types), cfg)
	if err != nil {
		return err
	}
	rate := 0.0
	if len(m.FrameRates) > 0 {
		rate = camera.PickFrameRate(m, cfg.FrameRate)
	}
	nt, ok := pickNative(types, m, rate)
	if !ok {
		return fmt.Errorf("%w: no native type for %s", camera.ErrUnsupported, m)
	}
	if err := s.setNative(nt.index); err != nil {
		return err
	}
	if out := outputFormat(m.Format, cfg); out != camera.Unknown {
		if !s.setOutput(out) && (out == camera.NV12 || !s.setOutput(camera.NV12)) {
			// No decoder or converter: deliver the native format and let
			// the public package convert.
			if err := s.setNative(nt.index); err != nil {
				return err
			}
		}
	}
	if err := s.refreshLayout(); err != nil {
		return err
	}
	if rate == 0 {
		rate = nt.rate
	}
	s.mode = camera.Mode{Format: s.layout.format, Width: s.layout.visible.w, Height: s.layout.visible.h}
	if rate > 0 {
		s.mode.FrameRates = []float64{rate}
		if s.clock.frameDur == 0 {
			s.clock.frameDur = int64(1e7/rate + 0.5)
		}
	}
	return nil
}

// setNative selects the device format: the native type at index becomes
// both the source format and the reader output.
func (s *stream) setNative(index uint32) error {
	var mt *sys.IMFMediaType
	if hr := s.reader.GetNativeMediaType(videoStream, index, &mt); hr.Failed() {
		return hrError("IMFSourceReader::GetNativeMediaType", hr)
	}
	defer mt.Release()
	if hr := s.reader.SetCurrentMediaType(videoStream, mt); hr.Failed() {
		return hrError("IMFSourceReader::SetCurrentMediaType", hr)
	}
	return nil
}

// setOutput asks the reader to deliver f, inserting a decoder or converter
// after the source. It reports whether the reader accepted.
func (s *stream) setOutput(f camera.PixelFormat) bool {
	sub, ok := subtypeFor(f)
	if !ok {
		return false
	}
	var mt *sys.IMFMediaType
	if hr := sys.MFCreateMediaType(&mt); hr.Failed() {
		return false
	}
	defer mt.Release()
	a := mt.Attributes()
	if a.SetGUID(&sys.MF_MT_MAJOR_TYPE, &sys.MFMediaType_Video).Failed() || a.SetGUID(&sys.MF_MT_SUBTYPE, &sub).Failed() {
		return false
	}
	return !s.reader.SetCurrentMediaType(videoStream, mt).Failed()
}

// refreshLayout reads the current output type.
func (s *stream) refreshLayout() error {
	var mt *sys.IMFMediaType
	if hr := s.reader.GetCurrentMediaType(videoStream, &mt); hr.Failed() {
		return hrError("IMFSourceReader::GetCurrentMediaType", hr)
	}
	defer mt.Release()
	a := mt.Attributes()
	var l layout
	g, _ := a.GUID(&sys.MF_MT_SUBTYPE)
	l.format = formatFromSubtype(g)
	if l.format == camera.Unknown {
		return fmt.Errorf("%w: output subtype %s", camera.ErrUnsupported, g)
	}
	v, ok := a.UINT64(&sys.MF_MT_FRAME_SIZE)
	if !ok {
		return &camera.BackendError{Backend: Name, Op: "GetCurrentMediaType", Err: errors.New("no MF_MT_FRAME_SIZE")}
	}
	w, h := unpack(v)
	l.width, l.height = int(w), int(h)
	l.visible = rect{w: l.width, h: l.height}
	if b, ok := a.Blob(&sys.MF_MT_MINIMUM_DISPLAY_APERTURE); ok {
		if r, ok := decodeVideoArea(b); ok && r.x+r.w <= l.width && r.y+r.h <= l.height {
			l.visible = r
		}
	}
	if st, ok := a.UINT32(&sys.MF_MT_DEFAULT_STRIDE); ok {
		l.stride = int(int32(st))
	}
	if v, ok := a.UINT64(&sys.MF_MT_FRAME_RATE); ok {
		if r := rateOf(unpack(v)); r > 0 {
			s.clock.frameDur = int64(1e7/r + 0.5)
		}
	}
	s.layout = l
	return nil
}

// readOne reads samples until one carries a picture.
func (s *stream) readOne() readResult {
	for {
		select {
		case <-s.done:
			return readResult{err: camera.ErrClosed}
		default:
		}
		var actual, flags uint32
		var ts int64
		var sample *sys.IMFSample
		hr := s.reader.ReadSample(videoStream, 0, &actual, &flags, &ts, &sample)
		if hr.Failed() {
			sample.Release()
			return readResult{err: hrError("IMFSourceReader::ReadSample", hr)}
		}
		if flags&sys.MF_SOURCE_READERF_ERROR != 0 {
			sample.Release()
			return readResult{err: hrError("IMFSourceReader::ReadSample", sys.E_FAIL)}
		}
		if flags&sys.MF_SOURCE_READERF_ENDOFSTREAM != 0 {
			sample.Release()
			return readResult{err: &camera.BackendError{Backend: Name, Op: "IMFSourceReader::ReadSample", Err: errors.New("end of stream (camera removed?)")}}
		}
		if flags&sys.MF_SOURCE_READERF_CURRENTMEDIATYPECHANGED != 0 {
			if err := s.refreshLayout(); err != nil {
				sample.Release()
				return readResult{err: err}
			}
		}
		if flags&sys.MF_SOURCE_READERF_STREAMTICK != 0 {
			s.clock.tick()
		}
		if sample == nil {
			continue
		}
		var st int64
		if !sample.GetSampleTime(&st).Failed() {
			ts = st
		}
		f, err := s.copySample(sample)
		sample.Release()
		if err != nil {
			return readResult{err: err}
		}
		f.Timestamp, f.Sequence = s.clock.next(ts)
		return readResult{f: f}
	}
}

// copySample copies the picture of a sample into Go memory.
func (s *stream) copySample(sample *sys.IMFSample) (*camera.Frame, error) {
	var buf *sys.IMFMediaBuffer
	var n uint32
	if !sample.GetBufferCount(&n).Failed() && n == 1 {
		if hr := sample.GetBufferByIndex(0, &buf); hr.Failed() {
			return nil, hrError("IMFSample::GetBufferByIndex", hr)
		}
	} else if hr := sample.ConvertToContiguousBuffer(&buf); hr.Failed() {
		return nil, hrError("IMFSample::ConvertToContiguousBuffer", hr)
	}
	defer buf.Release()

	l := s.layout
	f := &camera.Frame{Format: l.format, Width: l.visible.w, Height: l.visible.h}
	if l.format == camera.MJPEG {
		var p *byte
		var maxLen, cur uint32
		if hr := buf.Lock(&p, &maxLen, &cur); hr.Failed() {
			return nil, hrError("IMFMediaBuffer::Lock", hr)
		}
		data := make([]byte, cur)
		if cur > 0 {
			copy(data, unsafe.Slice(p, cur))
		}
		buf.Unlock()
		f.Width, f.Height = l.width, l.height
		f.Planes, f.Strides = [][]byte{data}, []int{0}
		return f, nil
	}
	planes, strides, err := copyRaw(buf, l)
	if err != nil {
		return nil, &camera.BackendError{Backend: Name, Op: "copy sample", Err: err}
	}
	f.Planes, f.Strides = planes, strides
	return f, nil
}

// copyRaw copies an uncompressed image, preferring the 2D-buffer interfaces
// that report the real pitch (and direction) of the rows.
func copyRaw(buf *sys.IMFMediaBuffer, l layout) ([][]byte, []int, error) {
	var b2 *sys.IMF2DBuffer2
	if buf.Unknown().QueryInterface(&sys.IID_IMF2DBuffer2, unsafe.Pointer(&b2)) == sys.S_OK {
		defer b2.Release()
		var scan0, start *byte
		var pitch int32
		var length uint32
		if hr := b2.Lock2DSize(sys.MF2DBuffer_LockFlags_Read, &scan0, &pitch, &start, &length); !hr.Failed() {
			defer b2.Unlock2D()
			if start == nil || scan0 == nil || length == 0 {
				return nil, nil, errors.New("empty 2D buffer")
			}
			base := int(uintptr(unsafe.Pointer(scan0)) - uintptr(unsafe.Pointer(start)))
			rows := l.height
			if base == 0 {
				rows = inferAllocRows(l.format, int(pitch), l.height, int(length))
			}
			ps, err := planesFor(l.format, int(pitch), rows, l.visible)
			if err != nil {
				return nil, nil, err
			}
			return copyPlanes(unsafe.Slice(start, length), base, ps)
		}
	}

	var b1 *sys.IMF2DBuffer
	if buf.Unknown().QueryInterface(&sys.IID_IMF2DBuffer, unsafe.Pointer(&b1)) == sys.S_OK {
		defer b1.Release()
		var scan0 *byte
		var pitch int32
		if hr := b1.Lock2D(&scan0, &pitch); !hr.Failed() {
			defer b1.Unlock2D()
			if scan0 == nil {
				return nil, nil, errors.New("empty 2D buffer")
			}
			ps, err := planesFor(l.format, int(pitch), l.height, l.visible)
			if err != nil {
				return nil, nil, err
			}
			// Lock2D does not report the buffer size: map exactly the bytes
			// the planes occupy.
			lo, hi := extent(ps)
			src := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(scan0), lo)), hi-lo)
			return copyPlanes(src, -lo, ps)
		}
	}

	var p *byte
	var maxLen, cur uint32
	if hr := buf.Lock(&p, &maxLen, &cur); hr.Failed() {
		return nil, nil, fmt.Errorf("IMFMediaBuffer::Lock: %s", hr)
	}
	defer buf.Unlock()
	if p == nil || cur == 0 {
		return nil, nil, errors.New("empty buffer")
	}
	stride := l.stride
	if stride == 0 {
		stride = tightStride(l.format, l.width)
	}
	base := 0
	if stride < 0 {
		// Bottom-up: the first row in display order is the last in memory.
		base = -stride * (l.height - 1)
	}
	ps, err := planesFor(l.format, stride, l.height, l.visible)
	if err != nil {
		return nil, nil, err
	}
	return copyPlanes(unsafe.Slice(p, cur), base, ps)
}

func (s *stream) Mode() camera.Mode {
	m := s.mode
	m.FrameRates = append([]float64(nil), m.FrameRates...)
	return m
}

func (s *stream) Read(ctx context.Context) (*camera.Frame, error) {
	select {
	case <-s.done:
		return nil, camera.ErrClosed
	default:
	}
	select {
	case s.sem <- struct{}{}:
	case <-s.done:
		return nil, camera.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.sem }()
	if !s.pending {
		select {
		case s.reqs <- struct{}{}:
			s.pending = true
		case <-s.done:
			return nil, camera.ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case r := <-s.results:
		s.pending = false
		if r.err != nil {
			return nil, r.err
		}
		return r.f, nil
	case <-s.done:
		return nil, camera.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err() // the result stays queued for the next Read
	}
}

// Close stops the worker. When the worker is stuck in ReadSample (a camera
// that stopped delivering), the source is shut down from here to unblock it.
func (s *stream) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	select {
	case <-s.exited:
		return nil
	case <-time.After(2 * time.Second):
	}
	s.forceShutdown()
	select {
	case <-s.exited:
	case <-time.After(5 * time.Second):
	}
	return nil
}

func (s *stream) forceShutdown() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		uninit, err := sys.CoInitializeMTA()
		if err != nil {
			return
		}
		if uninit {
			defer sys.CoUninitialize()
		}
		s.srcMu.Lock()
		if s.source != nil {
			s.source.Shutdown()
		}
		s.srcMu.Unlock()
	}()
	<-done
}

//go:build darwin

package avfoundation

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/shibukawa/hwcamera/internal/camera"
)

// frameQueue is how many frames wait for Read. A slow reader loses the
// oldest ones; Sequence keeps counting so the gap shows.
const frameQueue = 3

var queueLabel = []byte("com.github.shibukawa.hwcamera.capture\x00")

const delegateClassName = "HWCameraSampleBufferDelegate"

var (
	delegateOnce  sync.Once
	delegateClass objc.Class
	delegateErr   error
	// noopFn is a C-callable empty function for dispatch_sync_f, used to
	// wait until the delegate queue has finished any callback in flight.
	noopFn uintptr

	// streams maps a delegate instance to its stream. The delegate class is
	// shared by every stream, and keying by object keeps Go pointers out
	// of Objective-C memory.
	streams sync.Map // objc.ID -> *stream
)

// registerDelegate creates, once per process, the NSObject subclass that
// receives sample buffers and session notifications.
func registerDelegate() error {
	delegateOnce.Do(func() {
		var protos []*objc.Protocol
		// The protocol object only exists when some loaded image references
		// it; AVFoundation checks -respondsToSelector:, so conformance is a
		// courtesy, not a requirement.
		if p := objc.GetProtocol("AVCaptureVideoDataOutputSampleBufferDelegate"); p != nil {
			protos = append(protos, p)
		}
		delegateClass, delegateErr = objc.RegisterClass(delegateClassName, objc.GetClass("NSObject"), protos, nil, []objc.MethodDef{
			{Cmd: selDidOutput, Fn: didOutputSampleBuffer},
			{Cmd: selDidDrop, Fn: didDropSampleBuffer},
			{Cmd: selRuntimeError, Fn: sessionRuntimeError},
			{Cmd: selDisconnected, Fn: deviceDisconnected},
			{Cmd: selInterrupted, Fn: sessionInterrupted},
			{Cmd: selInterruptionEnded, Fn: sessionInterruptionEnded},
			{Cmd: selBecameActive, Fn: applicationDidBecomeActive},
		})
		noopFn = purego.NewCallback(func(uintptr) {})
	})
	return delegateErr
}

func streamOf(self objc.ID) *stream {
	v, ok := streams.Load(self)
	if !ok {
		return nil
	}
	return v.(*stream)
}

// didOutputSampleBuffer runs on the stream's serial dispatch queue. The
// sample buffer is only valid during the call, so the pixels are copied
// into Go memory here and the frame needs no Release.
func didOutputSampleBuffer(self objc.ID, _ objc.SEL, _, sbuf, _ objc.ID) {
	if s := streamOf(self); s != nil {
		s.deliver(uintptr(sbuf))
	}
}

func didDropSampleBuffer(self objc.ID, _ objc.SEL, _, _, _ objc.ID) {
	if s := streamOf(self); s != nil {
		s.seq.Add(1)
	}
}

func sessionRuntimeError(self objc.ID, _ objc.SEL, note objc.ID) {
	s := streamOf(self)
	if s == nil {
		return
	}
	var nserr objc.ID
	if note != 0 && avSessionErrorKey != 0 {
		if ui := note.Send(selUserInfo); ui != 0 {
			nserr = ui.Send(selObjectForKey, avSessionErrorKey)
		}
	}
	code, _ := nsError(nserr)
	switch code {
	case avErrorMediaServicesWereReset:
		// The media server died and came back (iOS): the session stopped
		// but can be started again, as Apple's AVCam sample does.
		// The NSError is only valid during this call: convert it now.
		go s.restart(nativeErr("session runtime error", nserr))
	case avErrorDeviceIsNotAvailableInBackground:
		// Started or running while the app was in the background (iOS):
		// restarted when the app becomes active again.
	default:
		s.fail(nativeErr("session runtime error", nserr))
	}
}

// sessionInterrupted observes AVCaptureSessionWasInterruptedNotification
// (iOS: the app went to the background, another app or a phone call took
// the camera, Split View, system pressure). Frames simply stop: Read keeps
// blocking until they resume.
func sessionInterrupted(self objc.ID, _ objc.SEL, _ objc.ID) {}

// sessionInterruptionEnded observes
// AVCaptureSessionInterruptionEndedNotification. AVFoundation resumes the
// session by itself; restart covers a session that stopped meanwhile.
func sessionInterruptionEnded(self objc.ID, _ objc.SEL, _ objc.ID) {
	if s := streamOf(self); s != nil {
		go s.restart(nil)
	}
}

// applicationDidBecomeActive observes
// UIApplicationDidBecomeActiveNotification (iOS only): an interruption is
// not guaranteed to end with a notification, and a session stopped by a
// background runtime error does not resume by itself.
func applicationDidBecomeActive(self objc.ID, _ objc.SEL, _ objc.ID) {
	if s := streamOf(self); s != nil {
		go s.restart(nil)
	}
}

// restart starts the session again if it is neither running nor
// interrupted. When it does not start, failure (if not nil) ends the
// stream; otherwise the stream keeps waiting for the next interruption end
// or app activation.
func (s *stream) restart(failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started.Load() || s.session == 0 {
		return
	}
	select {
	case <-s.done:
		return
	default:
	}
	ok := true
	withPool(func() {
		if objc.Send[bool](s.session, selIsRunning) || s.isInterrupted() {
			return
		}
		s.session.Send(selStartRunning)
		ok = objc.Send[bool](s.session, selIsRunning) || s.isInterrupted()
	})
	if !ok && failure != nil {
		s.fail(failure)
	}
}

// isInterrupted reports -[AVCaptureSession isInterrupted] (iOS 4,
// macOS 10.14; false where it does not exist).
func (s *stream) isInterrupted() bool {
	return responds(s.session, selIsInterrupted) && objc.Send[bool](s.session, selIsInterrupted)
}

func deviceDisconnected(self objc.ID, _ objc.SEL, _ objc.ID) {
	if s := streamOf(self); s != nil {
		s.fail(&camera.BackendError{Backend: Name, Op: "device disconnected", Err: camera.ErrNotFound})
	}
}

type stream struct {
	mode camera.Mode

	// mu serialises restart against teardown.
	mu sync.Mutex
	// started is set once openStream succeeded; restart does nothing
	// before.
	started atomic.Bool

	session, device, input, output, delegate objc.ID
	queue                                    uintptr
	locked                                   bool

	frames    chan *camera.Frame
	done      chan struct{}
	closeOnce sync.Once

	failed   chan struct{}
	failOnce sync.Once
	failErr  error

	seq atomic.Uint64
	// Touched only on the delegate queue.
	firstPTS int64
	havePTS  bool
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
		s.mu.Lock()
		withPool(s.teardown)
		s.mu.Unlock()
	})
	return nil
}

// teardown stops the session and releases everything openStream created;
// it copes with a partially built stream.
func (s *stream) teardown() {
	if s.delegate != 0 {
		class("NSNotificationCenter").Send(selDefaultCenter).Send(selRemoveObserver, s.delegate)
	}
	if s.session != 0 {
		// stopRunning is synchronous: no new buffers are delivered after it.
		s.session.Send(selStopRunning)
	}
	if s.locked && s.device != 0 {
		s.device.Send(selUnlockForConfiguration)
		s.locked = false
	}
	if s.output != 0 {
		s.output.Send(selSetDelegate, objc.ID(0), uintptr(0))
	}
	if s.queue != 0 {
		// Wait for a callback that was already running when the session
		// stopped, so the delegate is not freed under it.
		dispatchSyncF(s.queue, 0, noopFn)
	}
	if s.delegate != 0 {
		streams.Delete(s.delegate)
	}
	for _, o := range []objc.ID{s.output, s.input, s.session, s.delegate, s.device} {
		if o != 0 {
			o.Send(selRelease)
		}
	}
	s.output, s.input, s.session, s.delegate, s.device = 0, 0, 0, 0, 0
	if s.queue != 0 {
		dispatchRelease(s.queue)
		s.queue = 0
	}
}

func (s *stream) deliver(sbuf uintptr) {
	seq := s.seq.Add(1) - 1
	select {
	case <-s.done:
		return
	default:
	}
	pb := cmSampleBufferGetImageBuffer(sbuf)
	if pb == 0 {
		return
	}
	f := copyPixelBuffer(pb)
	if f == nil {
		return
	}
	pts := cmSampleBufferGetPresentationTimeStamp(sbuf)
	if pts.valid() {
		ns := pts.nanos()
		if !s.havePTS {
			s.firstPTS, s.havePTS = ns, true
		}
		f.Timestamp = time.Duration(ns - s.firstPTS)
	}
	f.Sequence = seq
	for {
		select {
		case s.frames <- f:
			return
		default:
		}
		// Full: drop the oldest. Only this queue sends, so after one
		// receive the send succeeds unless a reader raced us, in which case
		// the loop simply tries again.
		select {
		case <-s.frames:
		default:
		}
	}
}

// copyPixelBuffer copies a locked CVPixelBuffer into a tightly packed Go
// frame (Strides are the packed row sizes).
func copyPixelBuffer(pb uintptr) *camera.Frame {
	if cvPixelBufferLockBaseAddress(pb, cvLockReadOnly) != 0 {
		return nil
	}
	defer cvPixelBufferUnlockBaseAddress(pb, cvLockReadOnly)

	pf := formatFromFourCC(cvPixelBufferGetPixelFormatType(pb))
	if pf == camera.Unknown || pf == camera.MJPEG {
		return nil
	}
	f := &camera.Frame{
		Width:  int(cvPixelBufferGetWidth(pb)),
		Height: int(cvPixelBufferGetHeight(pb)),
		Format: pf,
	}
	n := int(cvPixelBufferGetPlaneCount(pb))
	if n == 0 {
		bpp := planeBytesPerPixel(pf, 0)
		plane, stride := copyPlane(cvPixelBufferGetBaseAddress(pb), int(cvPixelBufferGetBytesPerRow(pb)), f.Width*bpp, f.Height)
		if plane == nil {
			return nil
		}
		f.Planes, f.Strides = [][]byte{plane}, []int{stride}
		// Camera extensions (virtual cameras) fill BGRA with alpha 0 as
		// often as with 255.
		camera.OpaqueAlpha(f)
		return f
	}
	for p := range n {
		bpp := planeBytesPerPixel(pf, p)
		w := int(cvPixelBufferGetWidthOfPlane(pb, uintptr(p)))
		h := int(cvPixelBufferGetHeightOfPlane(pb, uintptr(p)))
		plane, stride := copyPlane(cvPixelBufferGetBaseAddressOfPlane(pb, uintptr(p)), int(cvPixelBufferGetBytesPerRowOfPlane(pb, uintptr(p))), w*bpp, h)
		if plane == nil {
			return nil
		}
		f.Planes = append(f.Planes, plane)
		f.Strides = append(f.Strides, stride)
	}
	return f
}

func copyPlane(base unsafe.Pointer, bpr, rowBytes, rows int) ([]byte, int) {
	if base == nil || bpr <= 0 || rows <= 0 || rowBytes <= 0 {
		return nil, 0
	}
	rowBytes = min(rowBytes, bpr)
	src := unsafe.Slice((*byte)(base), bpr*(rows-1)+rowBytes)
	if bpr == rowBytes {
		return append([]byte(nil), src...), rowBytes
	}
	dst := make([]byte, rowBytes*rows)
	for y := range rows {
		copy(dst[y*rowBytes:(y+1)*rowBytes], src[y*bpr:y*bpr+rowBytes])
	}
	return dst, rowBytes
}

func openStream(id string, cfg camera.Config) (s *stream, err error) {
	var dev objc.ID
	if id == "" {
		if ds := discover(); len(ds) > 0 {
			dev = ds[0]
		}
	} else {
		dev = class("AVCaptureDevice").Send(selDeviceWithUniqueID, nsString(id))
	}
	if dev == 0 {
		return nil, camera.ErrNotFound
	}

	formats := deviceFormats(dev)
	var modes []camera.Mode
	for _, f := range formats {
		modes = append(modes, f.mode)
	}
	mode, err := camera.ChooseMode(mergeModes(modes), cfg)
	if err != nil {
		return nil, err
	}
	rate := camera.PickFrameRate(mode, cfg.FrameRate)
	if len(mode.FrameRates) == 0 {
		rate = 0
	}
	outCC, outFmt := outputFourCC(cfg.Format)
	devFmt, rng, ok := pickFormat(formats, mode, rate, outCC)
	if !ok {
		return nil, camera.ErrUnsupported
	}
	if rate > 0 && !rangeCovers(rng.frameRange, rate) {
		// No format of this size reaches the rate: run at the best this
		// format has instead of raising an exception.
		rate = rng.MaxRate
	}

	s = &stream{
		mode:   camera.Mode{Format: outFmt, Width: mode.Width, Height: mode.Height},
		frames: make(chan *camera.Frame, frameQueue),
		done:   make(chan struct{}),
		failed: make(chan struct{}),
	}
	if rate > 0 {
		s.mode.FrameRates = []float64{rate}
	}
	defer func() {
		if err != nil {
			s.mu.Lock()
			s.teardown()
			s.mu.Unlock()
			s = nil
		}
	}()

	// Retain what lives past the autorelease pool.
	s.device = dev.Send(selRetain)

	var nserr objc.ID
	in := class("AVCaptureDeviceInput").Send(selDeviceInputWithDevice, dev, &nserr)
	if in == 0 {
		return s, nativeErr("deviceInputWithDevice", nserr)
	}
	s.input = in.Send(selRetain)

	s.session = class("AVCaptureSession").Send(selAlloc).Send(selInit)
	s.output = class("AVCaptureVideoDataOutput").Send(selAlloc).Send(selInit)
	s.delegate = objc.ID(delegateClass).Send(selAlloc).Send(selInit)
	if s.session == 0 || s.output == 0 || s.delegate == 0 {
		return s, &camera.BackendError{Backend: Name, Op: "alloc", Err: errors.New("object creation failed")}
	}
	s.queue = dispatchQueueCreate(&queueLabel[0], 0) // serial queue: callbacks in order
	streams.Store(s.delegate, s)

	settings := class("NSDictionary").Send(selDictionaryWithObjectForKey,
		class("NSNumber").Send(selNumberWithUnsignedInt, outCC), kCVPixelBufferPixelFormatTypeKey)
	s.output.Send(selSetVideoSettings, settings)
	s.output.Send(selSetAlwaysDiscards, true)
	s.output.Send(selSetDelegate, s.delegate, s.queue)

	center := class("NSNotificationCenter").Send(selDefaultCenter)
	if avSessionRuntimeErrorNotification != 0 {
		center.Send(selAddObserver, s.delegate, selRuntimeError, avSessionRuntimeErrorNotification, s.session)
	}
	if avDeviceWasDisconnectedNotification != 0 {
		center.Send(selAddObserver, s.delegate, selDisconnected, avDeviceWasDisconnectedNotification, dev)
	}
	if avSessionWasInterruptedNotification != 0 {
		center.Send(selAddObserver, s.delegate, selInterrupted, avSessionWasInterruptedNotification, s.session)
	}
	if avSessionInterruptionEndedNote != 0 {
		center.Send(selAddObserver, s.delegate, selInterruptionEnded, avSessionInterruptionEndedNote, s.session)
	}
	if uiAppDidBecomeActiveNotification != 0 {
		center.Send(selAddObserver, s.delegate, selBecameActive, uiAppDidBecomeActiveNotification, objc.ID(0))
	}

	// The session picks a format from its preset when it starts, which
	// would undo activeFormat. Holding the configuration lock across
	// -startRunning is the documented way to keep the device's own
	// settings, and the InputPriority preset tells the session to leave
	// the format alone afterwards.
	nserr = 0
	if !objc.Send[bool](dev, selLockForConfiguration, &nserr) {
		return s, nativeErr("lockForConfiguration", nserr)
	}
	s.locked = true

	s.session.Send(selBeginConfiguration)
	if !objc.Send[bool](s.session, selCanAddInput, s.input) {
		s.session.Send(selCommitConfiguration)
		return s, &camera.BackendError{Backend: Name, Op: "addInput", Err: camera.ErrBusy}
	}
	s.session.Send(selAddInput, s.input)
	if !objc.Send[bool](s.session, selCanAddOutput, s.output) {
		s.session.Send(selCommitConfiguration)
		return s, &camera.BackendError{Backend: Name, Op: "addOutput", Err: camera.ErrUnsupported}
	}
	s.session.Send(selAddOutput, s.output)
	if objc.Send[bool](s.session, selCanSetSessionPreset, avSessionPresetInputPriority) {
		s.session.Send(selSetSessionPreset, avSessionPresetInputPriority)
	}
	dev.Send(selSetActiveFormat, devFmt.obj)
	if rate > 0 {
		d := durationFor(rate, rng.frameRange, rng.minDur, rng.maxDur)
		dev.Send(selSetActiveMinFrameDuration, d)
		if responds(dev, selSetActiveMaxFrameDuration) {
			dev.Send(selSetActiveMaxFrameDuration, d)
		}
	}
	s.session.Send(selCommitConfiguration)

	s.session.Send(selStartRunning)
	dev.Send(selUnlockForConfiguration)
	s.locked = false
	// A session that starts interrupted (iOS: the app is in the background
	// or another app holds the camera) is not an error: frames come once
	// the interruption ends.
	if !objc.Send[bool](s.session, selIsRunning) && !s.isInterrupted() {
		return s, &camera.BackendError{Backend: Name, Op: "startRunning", Err: errors.New("session did not start")}
	}
	s.started.Store(true)
	return s, nil
}

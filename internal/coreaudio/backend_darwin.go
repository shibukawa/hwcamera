//go:build darwin

package coreaudio

import (
	"context"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/shibukawa/hwcamera/internal/audio"
)

// Backend is the Core Audio backend.
//
// Microphone access goes through macOS privacy control (TCC). An app
// bundle must carry NSMicrophoneUsageDescription in its Info.plist. A
// command-line binary uses the permission of the terminal application it
// runs in, which macOS asks for on first use. When access is denied the
// AudioQueue may still run and deliver silence, so Open checks the
// AVFoundation authorization status first.
//
// On iOS and iPadOS (GOOS=ios, CGO_ENABLED=1 for purego) capture is the
// same AudioQueue, but devices, routing and permission go through
// AVAudioSession, because the Core Audio HAL does not exist there:
//
//   - The app's Info.plist MUST carry NSMicrophoneUsageDescription, or iOS
//     kills the process when recording is requested. Permission is asked
//     with AVAudioApplication (iOS 17) or AVAudioSession; a denial is
//     audio.ErrPermission.
//   - Devices and Open set the shared audio session's category to
//     PlayAndRecord (mode Default, options AllowBluetooth and
//     DefaultToSpeaker) unless the app already chose PlayAndRecord or
//     Record, which are left as they are. Open activates the session and
//     never deactivates it (the app may be playing audio through it).
//     An app that changes the category later (e.g. to Playback or
//     Ambient) stops the recording.
//   - Devices lists AVAudioSession.availableInputs (built-in microphone,
//     wired or Bluetooth headset, USB interfaces); the ID is the port UID.
//     Default is the preferred input, else the input of the current route.
//     Open with an ID sets it as the preferred input; Open with "" leaves
//     the route as it is.
//   - When an interruption (phone call, Siri, another app's recording)
//     stops the queue, Read keeps blocking; the queue is started again
//     when the interruption ends or the app becomes active, and the next
//     buffer has Discontinuity set. A media services reset ends the
//     stream with audio.ErrNoDevice.
type Backend struct{}

func (Backend) Name() string { return Name }

func (Backend) Available() bool { return load() == nil }

func loadErrWrap(err error) error {
	return &audio.BackendError{Backend: Name, Op: "load", Err: err}
}

func osErr(op string, st int32, sentinel error) error {
	return &audio.BackendError{Backend: Name, Op: op, Code: int64(st), Err: sentinel}
}

func (Backend) Devices(ctx context.Context) ([]audio.DeviceInfo, error) {
	if err := load(); err != nil {
		return nil, loadErrWrap(err)
	}
	return platformDevices()
}

// authorize makes sure the process may record, asking the user when
// nobody has decided yet.
func authorize(ctx context.Context) error {
	switch authorizationStatus() {
	case authAuthorized, authUnknown:
		return nil
	case authDenied, authRestricted:
		return audio.ErrPermission
	}
	select {
	case <-requestAccess():
	case <-ctx.Done():
		return ctx.Err()
	}
	if authorizationStatus() != authAuthorized {
		return audio.ErrPermission
	}
	return nil
}

const (
	numQueueBuffers = 4
	ringBuffers     = 50
)

// streams maps the userData handle given to the AudioQueue to its stream.
var (
	streams    sync.Map // uintptr -> *stream
	nextHandle atomic.Uintptr
)

type stream struct {
	handle        uintptr
	queue         uintptr
	cfg           audio.Config
	bytesPerFrame int
	ring          *audio.Ring
	clk           clock // touched only by the AudioQueue callback thread

	closing   atomic.Bool
	closeOnce sync.Once

	// mu serialises restarting the queue after an interruption (iOS)
	// against Close.
	mu sync.Mutex
	// paused is set while the queue is stopped by an interruption (iOS);
	// resumed marks the next buffer as a discontinuity.
	paused  atomic.Bool
	resumed atomic.Bool
}

func (Backend) Open(ctx context.Context, id string, cfg audio.Config) (audio.Stream, error) {
	if err := load(); err != nil {
		return nil, loadErrWrap(err)
	}
	cfg.Normalize()
	queueDevice, err := prepareInput(ctx, id)
	if err != nil {
		return nil, err
	}

	s := &stream{
		handle:        nextHandle.Add(1),
		cfg:           cfg,
		bytesPerFrame: cfg.Format.Size() * cfg.Channels,
		ring:          audio.NewRing(ringBuffers),
		clk:           clock{host: timebase, rate: float64(cfg.SampleRate)},
	}
	streams.Store(s.handle, s)

	asbd := asbdFor(cfg)
	var q uintptr
	if st := audioQueueNewInput(&asbd, inputCallback, s.handle, 0, 0, 0, &q); st != 0 {
		streams.Delete(s.handle)
		return nil, osErr("AudioQueueNewInput", st, sentinelFor(st, false))
	}
	s.queue = q
	fail := func(op string, st int32, sentinel error) (audio.Stream, error) {
		s.closing.Store(true)
		audioQueueDispose(q, true)
		streams.Delete(s.handle)
		return nil, osErr(op, st, sentinel)
	}

	if queueDevice != "" {
		uid := cfStringCreateWithCString(0, queueDevice, kCFStringEncodingUTF8)
		if uid == 0 {
			return fail("CFStringCreateWithCString", 0, audio.ErrNotFound)
		}
		st := audioQueueSetProperty(q, kAudioQueueProperty_CurrentDevice, unsafe.Pointer(&uid), uint32(unsafe.Sizeof(uid)))
		cfRelease(uid)
		if st != 0 {
			return fail("AudioQueueSetProperty(CurrentDevice)", st, sentinelFor(st, false))
		}
	}
	if st := audioQueueAddPropertyListener(q, kAudioQueueProperty_IsRunning, propertyCallback, s.handle); st != 0 {
		return fail("AudioQueueAddPropertyListener", st, nil)
	}

	size := uint32(cfg.PeriodFrames() * s.bytesPerFrame)
	for range numQueueBuffers {
		var buf unsafe.Pointer
		if st := audioQueueAllocateBuffer(q, size, &buf); st != 0 {
			return fail("AudioQueueAllocateBuffer", st, nil)
		}
		if st := audioQueueEnqueueBuffer(q, buf, 0, nil); st != 0 {
			return fail("AudioQueueEnqueueBuffer", st, nil)
		}
	}
	if st := audioQueueStart(q, nil); st != 0 {
		return fail("AudioQueueStart", st, sentinelFor(st, true))
	}
	return s, nil
}

// sentinelFor maps an OSStatus to the package's sentinel errors. A device
// error while starting is what TCC denial produces when the authorization
// status could not be read or was granted in the meantime and revoked.
func sentinelFor(st int32, starting bool) error {
	switch st {
	case kAudioQueueErr_Permissions:
		return audio.ErrPermission
	case kAudioDevicePermissionsError:
		return audio.ErrBusy
	case kAudioQueueErr_InvalidDevice, kAudioHardwareBadDeviceError:
		if starting && authorizationStatus() != authAuthorized {
			return audio.ErrPermission
		}
		return audio.ErrNotFound
	}
	return nil
}

func (s *stream) Config() audio.Config { return s.cfg }

func (s *stream) Read(ctx context.Context) (*audio.Buffer, error) {
	if s.closing.Load() {
		return nil, audio.ErrClosed
	}
	return s.ring.Pop(ctx)
}

func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		s.mu.Lock()
		defer s.mu.Unlock()
		// Stop and Dispose with immediate=true return after the queue's
		// callbacks have finished; none run afterwards.
		audioQueueStop(s.queue, true)
		audioQueueDispose(s.queue, true)
		streams.Delete(s.handle)
		s.ring.Fail(audio.ErrClosed)
	})
	return nil
}

// onInput is the AudioQueueInputCallback:
//
//	void (*)(void *inUserData, AudioQueueRef inAQ, AudioQueueBufferRef inBuffer,
//	         const AudioTimeStamp *inStartTime, UInt32 inNumberPacketDescriptions,
//	         const AudioStreamPacketDescription *inPacketDescs)
func onInput(user, aq uintptr, bufRef, tsRef unsafe.Pointer, _ uint32, _ uintptr) {
	v, ok := streams.Load(user)
	if !ok || bufRef == nil {
		return
	}
	s := v.(*stream)
	if s.closing.Load() {
		return
	}
	buf := (*audioQueueBuffer)(bufRef)
	n := int(buf.AudioDataByteSize)
	n -= n % s.bytesPerFrame
	if n > 0 && buf.AudioData != 0 {
		data := make([]byte, n)
		copy(data, unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), buf.AudioData)), n))
		frames := n / s.bytesPerFrame
		b := &audio.Buffer{
			Data:       data,
			Format:     s.cfg.Format,
			SampleRate: s.cfg.SampleRate,
			Channels:   s.cfg.Channels,
			Frames:     frames,
		}
		if tsRef != nil {
			b.Timestamp, b.Discontinuity = s.clk.next(*(*audioTimeStamp)(tsRef), frames)
		}
		if s.resumed.Swap(false) {
			b.Discontinuity = true
		}
		s.ring.Push(b)
	}
	if !s.closing.Load() {
		audioQueueEnqueueBuffer(aq, bufRef, 0, nil)
	}
}

// onProperty is the AudioQueuePropertyListenerProc for IsRunning:
//
//	void (*)(void *inUserData, AudioQueueRef inAQ, AudioQueuePropertyID inID)
//
// The queue stops by itself when its device goes away (macOS) or an
// interruption begins (iOS); queueStopped decides what that means.
func onProperty(user, aq uintptr, id uint32) {
	if id != kAudioQueueProperty_IsRunning {
		return
	}
	v, ok := streams.Load(user)
	if !ok {
		return
	}
	s := v.(*stream)
	if s.closing.Load() {
		return
	}
	var running uint32
	size := uint32(unsafe.Sizeof(running))
	if st := audioQueueGetProperty(aq, kAudioQueueProperty_IsRunning, unsafe.Pointer(&running), &size); st != 0 || running != 0 {
		return
	}
	s.queueStopped()
}

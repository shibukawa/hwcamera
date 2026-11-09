//go:build darwin

package coreaudio

import (
	"context"
	"encoding/binary"
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
type Backend struct{}

func (Backend) Name() string { return Name }

func (Backend) Available() bool { return load() == nil }

func loadErrWrap(err error) error {
	return &audio.BackendError{Backend: Name, Op: "load", Err: err}
}

func osErr(op string, st int32, sentinel error) error {
	return &audio.BackendError{Backend: Name, Op: op, Code: int64(st), Err: sentinel}
}

type device struct {
	info audio.DeviceInfo
	obj  uint32
}

func (Backend) Devices(ctx context.Context) ([]audio.DeviceInfo, error) {
	if err := load(); err != nil {
		return nil, loadErrWrap(err)
	}
	devs, err := listDevices()
	if err != nil {
		return nil, err
	}
	out := make([]audio.DeviceInfo, len(devs))
	for i, d := range devs {
		out[i] = d.info
	}
	return out, nil
}

// listDevices returns the devices with at least one input channel, the
// default input first.
func listDevices() ([]device, error) {
	data, st := propertyData(kAudioObjectSystemObject, kAudioHardwarePropertyDevices, kAudioObjectPropertyScopeGlobal)
	if st != 0 {
		return nil, osErr("AudioObjectGetPropertyData(devices)", st, nil)
	}
	def, st := propertyU32(kAudioObjectSystemObject, kAudioHardwarePropertyDefaultInputDevice, kAudioObjectPropertyScopeGlobal)
	if st != 0 {
		def = kAudioObjectUnknown
	}
	var out []device
	for i := 0; i+4 <= len(data); i += 4 {
		obj := binary.LittleEndian.Uint32(data[i:])
		cfg, st := propertyData(obj, kAudioDevicePropertyStreamConfiguration, kAudioObjectPropertyScopeInput)
		if st != 0 {
			continue
		}
		ch := inputChannels(cfg)
		if ch == 0 {
			continue
		}
		uid, st := propertyString(obj, kAudioDevicePropertyDeviceUID, kAudioObjectPropertyScopeGlobal)
		if st != 0 || uid == "" {
			continue
		}
		name, _ := propertyString(obj, kAudioObjectPropertyName, kAudioObjectPropertyScopeGlobal)
		if name == "" {
			name = uid
		}
		rate, st := propertyF64(obj, kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal)
		if st != 0 {
			rate = 0
		}
		d := device{obj: obj, info: audio.DeviceInfo{
			ID:         uid,
			Name:       name,
			Backend:    Name,
			Default:    obj == def,
			SampleRate: int(rate + 0.5),
			Channels:   ch,
		}}
		if d.info.Default {
			out = append([]device{d}, out...)
		} else {
			out = append(out, d)
		}
	}
	return out, nil
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
}

func (Backend) Open(ctx context.Context, id string, cfg audio.Config) (audio.Stream, error) {
	if err := load(); err != nil {
		return nil, loadErrWrap(err)
	}
	cfg.Normalize()
	if id != "" {
		devs, err := listDevices()
		if err != nil {
			return nil, err
		}
		found := false
		for _, d := range devs {
			if d.info.ID == id {
				found = true
				break
			}
		}
		if !found {
			return nil, audio.ErrNotFound
		}
	} else if def, st := propertyU32(kAudioObjectSystemObject, kAudioHardwarePropertyDefaultInputDevice, kAudioObjectPropertyScopeGlobal); st == 0 && def == kAudioObjectUnknown {
		return nil, audio.ErrNoDevice
	}
	if err := authorize(ctx); err != nil {
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

	if id != "" {
		uid := cfStringCreateWithCString(0, id, kCFStringEncodingUTF8)
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
// The queue stops by itself when its device goes away.
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
	s.ring.Fail(&audio.BackendError{Backend: Name, Op: "AudioQueue stopped", Err: audio.ErrNoDevice})
}

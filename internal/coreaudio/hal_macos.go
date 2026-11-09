//go:build darwin && !ios

package coreaudio

import (
	"context"
	"encoding/binary"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/shibukawa/hwcamera/internal/audio"
)

// Core Audio HAL (CoreAudio.framework).
var (
	audioObjectGetPropertyDataSize func(obj uint32, addr *audioObjectPropertyAddress, qualSize uint32, qual unsafe.Pointer, size *uint32) int32
	audioObjectGetPropertyData     func(obj uint32, addr *audioObjectPropertyAddress, qualSize uint32, qual unsafe.Pointer, size *uint32, out unsafe.Pointer) int32
)

// AVFoundation, used only for the microphone authorization status. It is
// optional: without it Open relies on the AudioQueue status codes.
var (
	avLoaded         bool
	avMediaTypeAudio objc.ID

	selAuthorizationStatus, selRequestAccess objc.SEL
)

// loadPlatform binds the Core Audio HAL, which exists on macOS only (the
// iOS CoreAudio framework has no AudioObject API; binding it there would
// fail), and the optional AVFoundation authorization check.
func loadPlatform(open opener) error {
	ca, err := open("/System/Library/Frameworks/CoreAudio.framework/CoreAudio")
	if err != nil {
		return err
	}
	ca.fn(&audioObjectGetPropertyDataSize, "AudioObjectGetPropertyDataSize")
	ca.fn(&audioObjectGetPropertyData, "AudioObjectGetPropertyData")
	loadAVFoundation()
	return nil
}

// loadAVFoundation binds what authorizationStatus needs; failures only
// disable the check.
func loadAVFoundation() {
	h, err := purego.Dlopen("/System/Library/Frameworks/AVFoundation.framework/AVFoundation", rtldLazy|rtldGlobal)
	if err != nil {
		return
	}
	addr, err := purego.Dlsym(h, "AVMediaTypeAudio")
	if err != nil {
		return
	}
	// The symbol is the address of the NSString * variable.
	avMediaTypeAudio = *(*objc.ID)(unsafe.Add(unsafe.Pointer(nil), addr))
	if avMediaTypeAudio == 0 || objc.GetClass("AVCaptureDevice") == 0 || objc.GetClass("NSAutoreleasePool") == 0 {
		return
	}
	selAuthorizationStatus = objc.RegisterName("authorizationStatusForMediaType:")
	selRequestAccess = objc.RegisterName("requestAccessForMediaType:completionHandler:")
	avLoaded = true
}

// authorizationStatus returns the AVAuthorizationStatus for audio capture,
// or authUnknown without AVFoundation.
func authorizationStatus() int {
	if !avLoaded {
		return authUnknown
	}
	var st int
	withPool(func() {
		st = int(class("AVCaptureDevice").Send(selAuthorizationStatus, avMediaTypeAudio))
	})
	return st
}

// requestAccess asks the user for microphone access; the channel receives
// once the user (or the system) has answered.
func requestAccess() <-chan struct{} {
	ch := make(chan struct{}, 1)
	withPool(func() {
		// The BOOL argument is not trusted (upper register bits are
		// unspecified on x86_64); the caller reads the status again.
		blk := objc.NewBlock(func(_ objc.Block, _ bool) {
			select {
			case ch <- struct{}{}:
			default:
			}
		})
		class("AVCaptureDevice").Send(selRequestAccess, avMediaTypeAudio, blk)
		// AVFoundation copied the block; drop our reference.
		blk.Release()
	})
	return ch
}

// platformDevices lists the HAL input devices.
func platformDevices() ([]audio.DeviceInfo, error) {
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

type device struct {
	info audio.DeviceInfo
	obj  uint32
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

// prepareInput checks that the device exists (or that there is a default
// input), asks for permission, and returns the device UID the queue is to
// be bound to ("" for the default input).
func prepareInput(ctx context.Context, id string) (string, error) {
	if id != "" {
		devs, err := listDevices()
		if err != nil {
			return "", err
		}
		found := false
		for _, d := range devs {
			if d.info.ID == id {
				found = true
				break
			}
		}
		if !found {
			return "", audio.ErrNotFound
		}
	} else if def, st := propertyU32(kAudioObjectSystemObject, kAudioHardwarePropertyDefaultInputDevice, kAudioObjectPropertyScopeGlobal); st == 0 && def == kAudioObjectUnknown {
		return "", audio.ErrNoDevice
	}
	if err := authorize(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// queueStopped handles the queue stopping by itself, which on macOS means
// its device went away.
func (s *stream) queueStopped() {
	s.ring.Fail(&audio.BackendError{Backend: Name, Op: "AudioQueue stopped", Err: audio.ErrNoDevice})
}

// propertyData reads a variable-size HAL property.
func propertyData(obj, selector, scope uint32) ([]byte, int32) {
	addr := audioObjectPropertyAddress{selector, scope, kAudioObjectPropertyElementMain}
	var size uint32
	if st := audioObjectGetPropertyDataSize(obj, &addr, 0, nil, &size); st != 0 {
		return nil, st
	}
	if size == 0 {
		return nil, 0
	}
	buf := make([]byte, size)
	// Pointers inside the returned data (AudioBufferList.mData) are NULL
	// for the stream configuration; the buffer holds no Go pointers.
	if st := audioObjectGetPropertyData(obj, &addr, 0, nil, &size, unsafe.Pointer(&buf[0])); st != 0 {
		return nil, st
	}
	return buf[:size], 0
}

// propertyU32 reads a UInt32-sized property (an AudioObjectID).
func propertyU32(obj, selector, scope uint32) (uint32, int32) {
	addr := audioObjectPropertyAddress{selector, scope, kAudioObjectPropertyElementMain}
	var v uint32
	size := uint32(unsafe.Sizeof(v))
	st := audioObjectGetPropertyData(obj, &addr, 0, nil, &size, unsafe.Pointer(&v))
	return v, st
}

// propertyF64 reads a Float64 property.
func propertyF64(obj, selector, scope uint32) (float64, int32) {
	addr := audioObjectPropertyAddress{selector, scope, kAudioObjectPropertyElementMain}
	var v float64
	size := uint32(unsafe.Sizeof(v))
	st := audioObjectGetPropertyData(obj, &addr, 0, nil, &size, unsafe.Pointer(&v))
	return v, st
}

// propertyString reads a CFStringRef property (returned +1 retained) as a
// Go string.
func propertyString(obj, selector, scope uint32) (string, int32) {
	addr := audioObjectPropertyAddress{selector, scope, kAudioObjectPropertyElementMain}
	var ref uintptr
	size := uint32(unsafe.Sizeof(ref))
	if st := audioObjectGetPropertyData(obj, &addr, 0, nil, &size, unsafe.Pointer(&ref)); st != 0 {
		return "", st
	}
	if ref == 0 {
		return "", 0
	}
	defer cfRelease(ref)
	return cfString(ref), 0
}

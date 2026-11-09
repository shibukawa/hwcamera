//go:build darwin

package coreaudio

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const (
	rtldLazy   = 0x1
	rtldGlobal = 0x8
)

// Core Audio HAL (CoreAudio.framework).
var (
	audioObjectGetPropertyDataSize func(obj uint32, addr *audioObjectPropertyAddress, qualSize uint32, qual unsafe.Pointer, size *uint32) int32
	audioObjectGetPropertyData     func(obj uint32, addr *audioObjectPropertyAddress, qualSize uint32, qual unsafe.Pointer, size *uint32, out unsafe.Pointer) int32
)

// AudioQueue (AudioToolbox.framework).
var (
	audioQueueNewInput            func(asbd *audioStreamBasicDescription, cb uintptr, user uintptr, runLoop uintptr, mode uintptr, flags uint32, out *uintptr) int32
	audioQueueAllocateBuffer      func(aq uintptr, size uint32, out *unsafe.Pointer) int32
	audioQueueEnqueueBuffer       func(aq uintptr, buf unsafe.Pointer, numDescs uint32, descs unsafe.Pointer) int32
	audioQueueStart               func(aq uintptr, start unsafe.Pointer) int32
	audioQueueStop                func(aq uintptr, immediate bool) int32
	audioQueueDispose             func(aq uintptr, immediate bool) int32
	audioQueueSetProperty         func(aq uintptr, id uint32, data unsafe.Pointer, size uint32) int32
	audioQueueGetProperty         func(aq uintptr, id uint32, data unsafe.Pointer, size *uint32) int32
	audioQueueAddPropertyListener func(aq uintptr, id uint32, proc uintptr, user uintptr) int32
)

// CoreFoundation and libSystem.
var (
	cfStringGetLength                 func(s uintptr) int
	cfStringGetMaximumSizeForEncoding func(n int, enc uint32) int
	cfStringGetCString                func(s uintptr, buf *byte, size int, enc uint32) uint8
	cfStringCreateWithCString         func(alloc uintptr, s string, enc uint32) uintptr
	cfRelease                         func(obj uintptr)
	machTimebaseInfo                  func(info *hostClock) int32
)

// AVFoundation, used only for the microphone authorization status. It is
// optional: without it Open relies on the AudioQueue status codes.
var (
	avLoaded         bool
	avMediaTypeAudio objc.ID

	selNew, selDrain                         objc.SEL
	selAuthorizationStatus, selRequestAccess objc.SEL
)

// AVAuthorizationStatus values (AVCaptureDevice.h).
const (
	authNotDetermined = 0
	authRestricted    = 1
	authDenied        = 2
	authAuthorized    = 3
	authUnknown       = -1 // AVFoundation not available
)

var (
	loadOnce sync.Once
	loadErr  error

	timebase hostClock

	// The AudioQueue callbacks, created once: purego has a fixed number
	// of callback slots for the life of the process. Streams are found by
	// the userData handle.
	inputCallback    uintptr
	propertyCallback uintptr
)

func load() error {
	loadOnce.Do(func() { loadErr = doLoad() })
	return loadErr
}

type binder struct {
	handle uintptr
	lib    string
	errs   *[]string
}

func (b binder) fn(fptr any, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		*b.errs = append(*b.errs, b.lib+": "+name)
		return
	}
	purego.RegisterFunc(fptr, addr)
}

func doLoad() error {
	var errs []string
	open := func(path string) (binder, error) {
		h, err := purego.Dlopen(path, rtldLazy|rtldGlobal)
		if err != nil {
			return binder{}, fmt.Errorf("coreaudio: dlopen %s: %w", path, err)
		}
		return binder{handle: h, lib: path, errs: &errs}, nil
	}
	cf, err := open("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	if err != nil {
		return err
	}
	ca, err := open("/System/Library/Frameworks/CoreAudio.framework/CoreAudio")
	if err != nil {
		return err
	}
	at, err := open("/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox")
	if err != nil {
		return err
	}
	sys, err := open("/usr/lib/libSystem.B.dylib")
	if err != nil {
		return err
	}

	ca.fn(&audioObjectGetPropertyDataSize, "AudioObjectGetPropertyDataSize")
	ca.fn(&audioObjectGetPropertyData, "AudioObjectGetPropertyData")

	at.fn(&audioQueueNewInput, "AudioQueueNewInput")
	at.fn(&audioQueueAllocateBuffer, "AudioQueueAllocateBuffer")
	at.fn(&audioQueueEnqueueBuffer, "AudioQueueEnqueueBuffer")
	at.fn(&audioQueueStart, "AudioQueueStart")
	at.fn(&audioQueueStop, "AudioQueueStop")
	at.fn(&audioQueueDispose, "AudioQueueDispose")
	at.fn(&audioQueueSetProperty, "AudioQueueSetProperty")
	at.fn(&audioQueueGetProperty, "AudioQueueGetProperty")
	at.fn(&audioQueueAddPropertyListener, "AudioQueueAddPropertyListener")

	cf.fn(&cfStringGetLength, "CFStringGetLength")
	cf.fn(&cfStringGetMaximumSizeForEncoding, "CFStringGetMaximumSizeForEncoding")
	cf.fn(&cfStringGetCString, "CFStringGetCString")
	cf.fn(&cfStringCreateWithCString, "CFStringCreateWithCString")
	cf.fn(&cfRelease, "CFRelease")

	sys.fn(&machTimebaseInfo, "mach_timebase_info")

	if len(errs) > 0 {
		return errors.New("coreaudio: missing symbols: " + strings.Join(errs, ", "))
	}
	if machTimebaseInfo(&timebase) != 0 {
		timebase = hostClock{1, 1}
	}
	inputCallback = purego.NewCallback(onInput)
	propertyCallback = purego.NewCallback(onProperty)
	loadAVFoundation()
	return nil
}

// loadAVFoundation binds what authorizationStatus needs; failures only
// disable the check.
func loadAVFoundation() {
	if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", rtldLazy|rtldGlobal); err != nil {
		return
	}
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
	selNew, selDrain = objc.RegisterName("new"), objc.RegisterName("drain")
	selAuthorizationStatus = objc.RegisterName("authorizationStatusForMediaType:")
	selRequestAccess = objc.RegisterName("requestAccessForMediaType:completionHandler:")
	avLoaded = true
}

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// withPool runs fn inside an autorelease pool on a locked OS thread.
func withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := class("NSAutoreleasePool").Send(selNew)
	defer pool.Send(selDrain)
	fn()
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

// cfString converts a CFStringRef (nil-safe) to a Go string.
func cfString(s uintptr) string {
	if s == 0 {
		return ""
	}
	n := cfStringGetMaximumSizeForEncoding(cfStringGetLength(s), kCFStringEncodingUTF8) + 1
	if n <= 1 {
		return ""
	}
	buf := make([]byte, n)
	if cfStringGetCString(s, &buf[0], n, kCFStringEncodingUTF8) == 0 {
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
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

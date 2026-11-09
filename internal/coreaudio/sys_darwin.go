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

// opener opens a framework and returns a binder for its symbols.
type opener func(path string) (binder, error)

var selNew, selDrain objc.SEL

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
	at, err := open("/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox")
	if err != nil {
		return err
	}
	sys, err := open("/usr/lib/libSystem.B.dylib")
	if err != nil {
		return err
	}

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

	// Foundation for NSAutoreleasePool and NSString, needed by the
	// platform's Objective-C pieces.
	if _, err := open("/System/Library/Frameworks/Foundation.framework/Foundation"); err != nil {
		return err
	}
	selNew, selDrain = objc.RegisterName("new"), objc.RegisterName("drain")
	if err := loadPlatform(open); err != nil {
		return err
	}

	if len(errs) > 0 {
		return errors.New("coreaudio: missing symbols: " + strings.Join(errs, ", "))
	}
	if machTimebaseInfo(&timebase) != 0 {
		timebase = hostClock{1, 1}
	}
	inputCallback = purego.NewCallback(onInput)
	propertyCallback = purego.NewCallback(onProperty)
	return nil
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

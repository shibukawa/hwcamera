//go:build darwin

package avfoundation

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

// cmVideoDimensions mirrors CMVideoDimensions (two int32, returned in one
// register on both architectures).
type cmVideoDimensions struct {
	Width  int32
	Height int32
}

// AVAuthorizationStatus values (AVCaptureDevice.h).
const (
	authNotDetermined = 0
	authRestricted    = 1
	authDenied        = 2
	authAuthorized    = 3
)

// AVCaptureDevicePosition values.
const (
	avPositionUnspecified = 0
	avPositionBack        = 1
	avPositionFront       = 2
)

// kCVPixelBufferLock_ReadOnly.
const cvLockReadOnly uint64 = 1

var (
	cmSampleBufferGetImageBuffer           func(sbuf uintptr) uintptr
	cmSampleBufferGetPresentationTimeStamp func(sbuf uintptr) cmTime
	cmFormatDescriptionGetMediaSubType     func(desc uintptr) uint32
	cmVideoFormatDescriptionGetDimensions  func(desc uintptr) cmVideoDimensions

	cvPixelBufferLockBaseAddress       func(pb uintptr, flags uint64) int32
	cvPixelBufferUnlockBaseAddress     func(pb uintptr, flags uint64) int32
	cvPixelBufferGetPixelFormatType    func(pb uintptr) uint32
	cvPixelBufferGetWidth              func(pb uintptr) uintptr
	cvPixelBufferGetHeight             func(pb uintptr) uintptr
	cvPixelBufferGetPlaneCount         func(pb uintptr) uintptr
	cvPixelBufferGetBaseAddress        func(pb uintptr) unsafe.Pointer
	cvPixelBufferGetBytesPerRow        func(pb uintptr) uintptr
	cvPixelBufferGetBaseAddressOfPlane func(pb uintptr, plane uintptr) unsafe.Pointer
	cvPixelBufferGetBytesPerRowOfPlane func(pb uintptr, plane uintptr) uintptr
	cvPixelBufferGetWidthOfPlane       func(pb uintptr, plane uintptr) uintptr
	cvPixelBufferGetHeightOfPlane      func(pb uintptr, plane uintptr) uintptr

	dispatchQueueCreate func(label *byte, attr uintptr) uintptr
	dispatchRelease     func(obj uintptr)
	dispatchSyncF       func(queue uintptr, ctx uintptr, fn uintptr)
)

// NSString and CFString constants exported by the frameworks. The optional
// ones are 0 on macOS releases that predate them.
var (
	kCVPixelBufferPixelFormatTypeKey objc.ID
	avMediaTypeVideo                 objc.ID
	avSessionPresetInputPriority     objc.ID
	avDeviceTypeBuiltInWideAngle     objc.ID
	avDeviceTypeExternal             objc.ID // macOS 14
	avDeviceTypeExternalUnknown      objc.ID // macOS 10.15, deprecated in 14
	avDeviceTypeContinuity           objc.ID // macOS 14

	avSessionRuntimeErrorNotification   objc.ID
	avSessionErrorKey                   objc.ID
	avDeviceWasDisconnectedNotification objc.ID
)

var (
	loadOnce sync.Once
	loadErr  error
)

// load opens the frameworks and binds every symbol; the result is cached.
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

// obj reads an exported object pointer (an NSString * or CFStringRef
// global): the symbol is the address of the variable, not the object.
func (b binder) obj(dst *objc.ID, name string, optional bool) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		if !optional {
			*b.errs = append(*b.errs, b.lib+": "+name)
		}
		return
	}
	*dst = *(*objc.ID)(unsafe.Add(unsafe.Pointer(nil), addr))
}

func doLoad() error {
	var errs []string
	open := func(path string) (binder, error) {
		h, err := purego.Dlopen(path, rtldLazy|rtldGlobal)
		if err != nil {
			return binder{}, fmt.Errorf("avfoundation: dlopen %s: %w", path, err)
		}
		return binder{handle: h, lib: path, errs: &errs}, nil
	}
	// Foundation first: AVFoundation's classes derive from NSObject and the
	// helpers below need NSString, NSArray and NSAutoreleasePool.
	if _, err := open("/System/Library/Frameworks/Foundation.framework/Foundation"); err != nil {
		return err
	}
	av, err := open("/System/Library/Frameworks/AVFoundation.framework/AVFoundation")
	if err != nil {
		return err
	}
	cm, err := open("/System/Library/Frameworks/CoreMedia.framework/CoreMedia")
	if err != nil {
		return err
	}
	cv, err := open("/System/Library/Frameworks/CoreVideo.framework/CoreVideo")
	if err != nil {
		return err
	}
	sys, err := open("/usr/lib/libSystem.B.dylib")
	if err != nil {
		return err
	}

	cm.fn(&cmSampleBufferGetImageBuffer, "CMSampleBufferGetImageBuffer")
	cm.fn(&cmSampleBufferGetPresentationTimeStamp, "CMSampleBufferGetPresentationTimeStamp")
	cm.fn(&cmFormatDescriptionGetMediaSubType, "CMFormatDescriptionGetMediaSubType")
	cm.fn(&cmVideoFormatDescriptionGetDimensions, "CMVideoFormatDescriptionGetDimensions")

	cv.fn(&cvPixelBufferLockBaseAddress, "CVPixelBufferLockBaseAddress")
	cv.fn(&cvPixelBufferUnlockBaseAddress, "CVPixelBufferUnlockBaseAddress")
	cv.fn(&cvPixelBufferGetPixelFormatType, "CVPixelBufferGetPixelFormatType")
	cv.fn(&cvPixelBufferGetWidth, "CVPixelBufferGetWidth")
	cv.fn(&cvPixelBufferGetHeight, "CVPixelBufferGetHeight")
	cv.fn(&cvPixelBufferGetPlaneCount, "CVPixelBufferGetPlaneCount")
	cv.fn(&cvPixelBufferGetBaseAddress, "CVPixelBufferGetBaseAddress")
	cv.fn(&cvPixelBufferGetBytesPerRow, "CVPixelBufferGetBytesPerRow")
	cv.fn(&cvPixelBufferGetBaseAddressOfPlane, "CVPixelBufferGetBaseAddressOfPlane")
	cv.fn(&cvPixelBufferGetBytesPerRowOfPlane, "CVPixelBufferGetBytesPerRowOfPlane")
	cv.fn(&cvPixelBufferGetWidthOfPlane, "CVPixelBufferGetWidthOfPlane")
	cv.fn(&cvPixelBufferGetHeightOfPlane, "CVPixelBufferGetHeightOfPlane")
	cv.obj(&kCVPixelBufferPixelFormatTypeKey, "kCVPixelBufferPixelFormatTypeKey", false)

	sys.fn(&dispatchQueueCreate, "dispatch_queue_create")
	sys.fn(&dispatchRelease, "dispatch_release")
	sys.fn(&dispatchSyncF, "dispatch_sync_f")

	av.obj(&avMediaTypeVideo, "AVMediaTypeVideo", false)
	av.obj(&avSessionPresetInputPriority, "AVCaptureSessionPresetInputPriority", false)
	av.obj(&avDeviceTypeBuiltInWideAngle, "AVCaptureDeviceTypeBuiltInWideAngleCamera", true)
	av.obj(&avDeviceTypeExternal, "AVCaptureDeviceTypeExternal", true)
	av.obj(&avDeviceTypeExternalUnknown, "AVCaptureDeviceTypeExternalUnknown", true)
	av.obj(&avDeviceTypeContinuity, "AVCaptureDeviceTypeContinuityCamera", true)
	av.obj(&avSessionRuntimeErrorNotification, "AVCaptureSessionRuntimeErrorNotification", true)
	av.obj(&avSessionErrorKey, "AVCaptureSessionErrorKey", true)
	av.obj(&avDeviceWasDisconnectedNotification, "AVCaptureDeviceWasDisconnectedNotification", true)

	if len(errs) > 0 {
		return errors.New("avfoundation: missing symbols: " + strings.Join(errs, ", "))
	}
	if objc.GetClass("AVCaptureDevice") == 0 || objc.GetClass("AVCaptureSession") == 0 {
		return errors.New("avfoundation: AVCaptureDevice class not found")
	}
	initSelectors()
	return nil
}

// Selectors, registered once: sel_registerName takes the runtime lock.
var (
	selAlloc, selInit, selNew, selRelease, selRetain, selDrain          objc.SEL
	selCount, selObjectAtIndex, selUTF8String, selIsEqualToString       objc.SEL
	selStringWithUTF8String, selArrayWithObjectsCount                   objc.SEL
	selNumberWithUnsignedInt, selDictionaryWithObjectForKey             objc.SEL
	selRespondsToSelector, selLocalizedDescription, selCode             objc.SEL
	selMainBundle, selObjectForInfoDictionaryKey, selBoolValue          objc.SEL
	selDiscoverySession, selDevices, selDevicesWithMediaType            objc.SEL
	selDeviceWithUniqueID, selUniqueID, selLocalizedName                objc.SEL
	selPosition, selDeviceType, selFormats, selFormatDescription        objc.SEL
	selFrameRateRanges, selMinFrameRate, selMaxFrameRate                objc.SEL
	selMinFrameDuration, selMaxFrameDuration                            objc.SEL
	selAuthorizationStatus, selRequestAccess                            objc.SEL
	selDeviceInputWithDevice, selCanAddInput, selAddInput               objc.SEL
	selCanAddOutput, selAddOutput, selBeginConfiguration                objc.SEL
	selCommitConfiguration, selSetSessionPreset, selCanSetSessionPreset objc.SEL
	selLockForConfiguration, selUnlockForConfiguration                  objc.SEL
	selSetActiveFormat, selSetActiveMinFrameDuration                    objc.SEL
	selSetActiveMaxFrameDuration                                        objc.SEL
	selSetVideoSettings, selSetAlwaysDiscards, selSetDelegate           objc.SEL
	selStartRunning, selStopRunning, selIsRunning                       objc.SEL
	selRemoveInput, selRemoveOutput                                     objc.SEL
	selDidOutput, selDidDrop                                            objc.SEL
	selRuntimeError, selDisconnected, selDefaultCenter                  objc.SEL
	selAddObserver, selRemoveObserver, selUserInfo, selObjectForKey     objc.SEL
)

func initSelectors() {
	r := objc.RegisterName
	selAlloc, selInit, selNew = r("alloc"), r("init"), r("new")
	selRelease, selRetain, selDrain = r("release"), r("retain"), r("drain")
	selCount, selObjectAtIndex = r("count"), r("objectAtIndex:")
	selUTF8String, selIsEqualToString = r("UTF8String"), r("isEqualToString:")
	selStringWithUTF8String = r("stringWithUTF8String:")
	selArrayWithObjectsCount = r("arrayWithObjects:count:")
	selNumberWithUnsignedInt = r("numberWithUnsignedInt:")
	selDictionaryWithObjectForKey = r("dictionaryWithObject:forKey:")
	selRespondsToSelector = r("respondsToSelector:")
	selLocalizedDescription, selCode = r("localizedDescription"), r("code")
	selMainBundle, selObjectForInfoDictionaryKey = r("mainBundle"), r("objectForInfoDictionaryKey:")
	selBoolValue = r("boolValue")
	selDiscoverySession = r("discoverySessionWithDeviceTypes:mediaType:position:")
	selDevices, selDevicesWithMediaType = r("devices"), r("devicesWithMediaType:")
	selDeviceWithUniqueID = r("deviceWithUniqueID:")
	selUniqueID, selLocalizedName = r("uniqueID"), r("localizedName")
	selPosition, selDeviceType = r("position"), r("deviceType")
	selFormats, selFormatDescription = r("formats"), r("formatDescription")
	selFrameRateRanges = r("videoSupportedFrameRateRanges")
	selMinFrameRate, selMaxFrameRate = r("minFrameRate"), r("maxFrameRate")
	selMinFrameDuration, selMaxFrameDuration = r("minFrameDuration"), r("maxFrameDuration")
	selAuthorizationStatus = r("authorizationStatusForMediaType:")
	selRequestAccess = r("requestAccessForMediaType:completionHandler:")
	selDeviceInputWithDevice = r("deviceInputWithDevice:error:")
	selCanAddInput, selAddInput = r("canAddInput:"), r("addInput:")
	selCanAddOutput, selAddOutput = r("canAddOutput:"), r("addOutput:")
	selRemoveInput, selRemoveOutput = r("removeInput:"), r("removeOutput:")
	selBeginConfiguration, selCommitConfiguration = r("beginConfiguration"), r("commitConfiguration")
	selSetSessionPreset, selCanSetSessionPreset = r("setSessionPreset:"), r("canSetSessionPreset:")
	selLockForConfiguration, selUnlockForConfiguration = r("lockForConfiguration:"), r("unlockForConfiguration")
	selSetActiveFormat = r("setActiveFormat:")
	selSetActiveMinFrameDuration = r("setActiveVideoMinFrameDuration:")
	selSetActiveMaxFrameDuration = r("setActiveVideoMaxFrameDuration:")
	selSetVideoSettings = r("setVideoSettings:")
	selSetAlwaysDiscards = r("setAlwaysDiscardsLateVideoFrames:")
	selSetDelegate = r("setSampleBufferDelegate:queue:")
	selStartRunning, selStopRunning, selIsRunning = r("startRunning"), r("stopRunning"), r("isRunning")
	selDidOutput = r("captureOutput:didOutputSampleBuffer:fromConnection:")
	selDidDrop = r("captureOutput:didDropSampleBuffer:fromConnection:")
	// Our own notification handlers; prefixed so they cannot collide with
	// anything NSObject or a category might define.
	selRuntimeError = r("hwcameraSessionRuntimeError:")
	selDisconnected = r("hwcameraDeviceDisconnected:")
	selDefaultCenter = r("defaultCenter")
	selAddObserver = r("addObserver:selector:name:object:")
	selRemoveObserver = r("removeObserver:")
	selUserInfo, selObjectForKey = r("userInfo"), r("objectForKey:")
}

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// withPool runs fn inside an autorelease pool on a locked OS thread.
// Goroutine threads have no pool of their own, so every autoreleased
// object returned by a factory method (+stringWithUTF8String:,
// +discoverySession..., -formats) would otherwise leak; the pool has to be
// drained on the thread that created it, hence the lock.
func withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := class("NSAutoreleasePool").Send(selNew)
	defer pool.Send(selDrain)
	fn()
}

func nsString(s string) objc.ID {
	return class("NSString").Send(selStringWithUTF8String, s)
}

func goString(s objc.ID) string {
	if s == 0 {
		return ""
	}
	p := unsafe.Add(unsafe.Pointer(nil), uintptr(s.Send(selUTF8String)))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

func items(arr objc.ID) []objc.ID {
	if arr == 0 {
		return nil
	}
	n := int(arr.Send(selCount))
	out := make([]objc.ID, n)
	for i := range out {
		out[i] = arr.Send(selObjectAtIndex, uintptr(i))
	}
	return out
}

func nsArray(objs []objc.ID) objc.ID {
	if len(objs) == 0 {
		return class("NSArray").Send(selArrayWithObjectsCount, unsafe.Pointer(nil), uintptr(0))
	}
	arr := class("NSArray").Send(selArrayWithObjectsCount, unsafe.Pointer(&objs[0]), uintptr(len(objs)))
	runtime.KeepAlive(objs)
	return arr
}

func responds(obj objc.ID, sel objc.SEL) bool {
	return obj != 0 && objc.Send[bool](obj, selRespondsToSelector, sel)
}

func equalStrings(a, b objc.ID) bool {
	return a != 0 && b != 0 && objc.Send[bool](a, selIsEqualToString, b)
}

// nsError turns an NSError into a Go error (nil for nil).
func nsError(e objc.ID) (int64, error) {
	if e == 0 {
		return 0, nil
	}
	return int64(e.Send(selCode)), errors.New(goString(e.Send(selLocalizedDescription)))
}

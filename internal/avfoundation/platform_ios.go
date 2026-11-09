//go:build ios

package avfoundation

import (
	"sort"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// loadPlatform binds UIApplicationDidBecomeActiveNotification. UIKit is
// loaded in every iOS app; the notification is optional (without it a
// session that stopped while the app was in the background is restarted
// only by AVCaptureSessionInterruptionEndedNotification).
func loadPlatform(open func(string) (binder, error)) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/UIKit.framework/UIKit", rtldLazy|rtldGlobal); err != nil {
		return
	}
	if addr, err := purego.Dlsym(purego.RTLD_DEFAULT, "UIApplicationDidBecomeActiveNotification"); err == nil && addr != 0 {
		uiAppDidBecomeActiveNotification = *(*objc.ID)(unsafe.Add(unsafe.Pointer(nil), addr))
	}
}

// discoveryTypes returns the physical camera types of iOS. The virtual
// multi-camera devices (BuiltInDualCamera, BuiltInDualWideCamera,
// BuiltInTripleCamera) and BuiltInTrueDepthCamera are deliberately left
// out: their video is that of a physical camera listed here (the dual and
// triple cameras switch between the back cameras with zoom, which this
// library does not control; TrueDepth's colour camera is the front
// wide-angle camera), so listing them would show the same camera twice.
// AVCaptureDeviceTypeExternal is iPadOS 17 and later; the symbol is
// missing (0) before that and the type is then not asked for.
func discoveryTypes() []objc.ID {
	var types []objc.ID
	for _, t := range []objc.ID{avDeviceTypeBuiltInWideAngle, avDeviceTypeBuiltInUltraWide,
		avDeviceTypeBuiltInTelephoto, avDeviceTypeExternal} {
		if t != 0 {
			types = append(types, t)
		}
	}
	return types
}

// orderDevices sorts the cameras for automatic choice: back wide, front
// wide, ultra-wide, telephoto, external (see iosDeviceOrder).
func orderDevices(ds []objc.ID) []objc.ID {
	keys := make(map[objc.ID]int, len(ds))
	for _, d := range ds {
		keys[d] = iosDeviceOrder(iosTypeOf(d), int(d.Send(selPosition)))
	}
	sort.SliceStable(ds, func(i, j int) bool { return keys[ds[i]] < keys[ds[j]] })
	return ds
}

func iosTypeOf(d objc.ID) iosType {
	if !responds(d, selDeviceType) {
		return iosTypeOther
	}
	t := d.Send(selDeviceType)
	switch {
	case equalStrings(t, avDeviceTypeBuiltInWideAngle):
		return iosTypeWide
	case equalStrings(t, avDeviceTypeBuiltInUltraWide):
		return iosTypeUltraWide
	case equalStrings(t, avDeviceTypeBuiltInTelephoto):
		return iosTypeTelephoto
	case equalStrings(t, avDeviceTypeExternal):
		return iosTypeExternal
	}
	return iosTypeOther
}

// isVirtual is always false on iOS: -transportType does not exist there
// and iOS has no camera extensions.
func isVirtual(objc.ID) bool { return false }

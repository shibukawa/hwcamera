//go:build darwin && !ios

package avfoundation

import "github.com/ebitengine/purego/objc"

// loadPlatform binds the macOS-only symbols (none beyond the shared ones).
func loadPlatform(func(string) (binder, error)) {}

// discoveryTypes returns the device types to discover on macOS: the
// built-in camera, external (USB, camera extension) cameras, and
// Continuity Camera when the app opted in.
func discoveryTypes() []objc.ID {
	var types []objc.ID
	if avDeviceTypeBuiltInWideAngle != 0 {
		types = append(types, avDeviceTypeBuiltInWideAngle)
	}
	if avDeviceTypeExternal != 0 {
		types = append(types, avDeviceTypeExternal)
	} else if avDeviceTypeExternalUnknown != 0 {
		types = append(types, avDeviceTypeExternalUnknown)
	}
	if avDeviceTypeContinuity != 0 && continuityAllowed() {
		types = append(types, avDeviceTypeContinuity)
	}
	return types
}

// orderDevices keeps AVFoundation's order on macOS.
func orderDevices(ds []objc.ID) []objc.ID { return ds }

// continuityAllowed reports whether the main bundle opts into Continuity
// Camera. macOS 14 only lets apps that set
// NSCameraUseContinuityCameraDeviceType in Info.plist use that device type,
// and asking for it without the key is not something to risk an
// Objective-C exception on.
func continuityAllowed() bool {
	b := class("NSBundle").Send(selMainBundle)
	if b == 0 {
		return false
	}
	v := b.Send(selObjectForInfoDictionaryKey, nsString("NSCameraUseContinuityCameraDeviceType"))
	return v != 0 && responds(v, selBoolValue) && objc.Send[bool](v, selBoolValue)
}

// isVirtual reports a software camera: -transportType (an
// IOAudioDeviceTransportType FourCC) is 'virt' for camera extensions and
// DAL plug-ins that are not hardware (OBS, VR and conferencing drivers).
func isVirtual(d objc.ID) bool {
	if !responds(d, selTransportType) {
		return false
	}
	return uint32(objc.Send[int32](d, selTransportType)) == transportVirtual
}

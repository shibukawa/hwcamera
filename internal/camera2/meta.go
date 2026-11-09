// Package camera2 is the Android camera backend: the NDK Camera2 C API
// (libcamera2ndk.so, API 24) with an AImageReader (libmediandk.so) as the
// capture target, both loaded at run time with purego. No cgo source is
// involved, but purego needs the cgo runtime on Android, so the app is
// built with CGO_ENABLED=1 and the NDK toolchain (gomobile / ebitenmobile).
//
// Frames are requested as AIMAGE_FORMAT_YUV_420_888, the format every
// Camera2 device must offer at every output size, and copied into Go-owned
// NV12 in the image listener (the AImage goes back to the reader at once),
// so Frame.Release is a no-op.
//
// The app must hold android.permission.CAMERA, declared in its manifest
// and granted at run time from Java or Kotlin (ActivityCompat.
// requestPermissions) before Open; otherwise Open fails with ErrPermission.
// Listing cameras works without the permission.
//
// Frames come in the sensor's orientation (ACAMERA_SENSOR_ORIENTATION,
// usually 90 or 270 degrees from the phone's natural portrait): a phone
// held upright gets landscape frames, which the app rotates for display.
//
// This file holds the constants and the pure-Go parsing of camera
// characteristics, which compile everywhere so they can be tested on the
// desktop; the bindings (*_ndk.go) build for android, or on any OS with
// the hwcamera_typecheck tag so that they can be type-checked without the
// NDK: GOOS=linux GOARCH=arm64 go vet -tags hwcamera_typecheck.
package camera2

import (
	"errors"
	"fmt"
	"sort"
	"unsafe"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// Name is the backend name.
const Name = "camera2"

// Metadata tags (camera/NdkCameraMetadataTags.h): section << 16 | index.
const (
	sectionControl = 1
	sectionLens    = 8
	sectionScaler  = 13
	sectionSensor  = 14
	sectionInfo    = 21

	tagControlAETargetFPSRange          = sectionControl<<16 | 5  // ACAMERA_CONTROL_AE_TARGET_FPS_RANGE
	tagControlAEAvailableTargetFPSRange = sectionControl<<16 | 20 // ACAMERA_CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES
	tagLensFacing                       = sectionLens<<16 | 5     // ACAMERA_LENS_FACING
	tagScalerStreamConfigurations       = sectionScaler<<16 | 10  // ACAMERA_SCALER_AVAILABLE_STREAM_CONFIGURATIONS
	tagScalerMinFrameDurations          = sectionScaler<<16 | 11  // ACAMERA_SCALER_AVAILABLE_MIN_FRAME_DURATIONS
	tagSensorOrientation                = sectionSensor<<16 | 14  // ACAMERA_SENSOR_ORIENTATION
	tagInfoSupportedHardwareLevel       = sectionInfo<<16 | 0     // ACAMERA_INFO_SUPPORTED_HARDWARE_LEVEL
)

// ACAMERA_LENS_FACING values.
const (
	facingFront    = 0
	facingBack     = 1
	facingExternal = 2
)

// Metadata entry types (ACAMERA_TYPE_*).
const (
	typeByte     = 0
	typeInt32    = 1
	typeFloat    = 2
	typeInt64    = 3
	typeDouble   = 4
	typeRational = 5
)

// AIMAGE_FORMAT_* (media/NdkImage.h).
const (
	formatYUV420888 = 0x23
	formatJPEG      = 0x100
)

// ACAMERA_SCALER_AVAILABLE_STREAM_CONFIGURATIONS_{OUTPUT,INPUT}.
const configOutput = 0

// ACameraDevice_request_template.
const (
	templatePreview = 1
	templateRecord  = 3
)

// camera_status_t (camera/NdkCameraError.h).
const (
	statusOK                = 0
	errUnknown              = -10000
	errInvalidParameter     = -10001
	errCameraDisconnected   = -10002
	errNotEnoughMemory      = -10003
	errMetadataNotFound     = -10004
	errCameraDevice         = -10005
	errCameraService        = -10006
	errSessionClosed        = -10007
	errInvalidOperation     = -10008
	errStreamConfigureFail  = -10009
	errCameraInUse          = -10010
	errMaxCameraInUse       = -10011
	errCameraDisabled       = -10012
	errPermissionDenied     = -10013
	errUnsupportedOperation = -10014
)

// ACameraDevice_ErrorStateCallback codes (ERROR_CAMERA_*).
const (
	devErrInUse    = 1
	devErrMaxInUse = 2
	devErrDisabled = 3
	devErrDevice   = 4
	devErrService  = 5
)

// media_status_t values met with AImageReader (media/NdkMediaError.h).
const (
	mediaOK                = 0
	mediaNoBufferAvailable = -30001 // AMEDIA_IMGREADER_NO_BUFFER_AVAILABLE
	mediaMaxImagesAcquired = -30002 // AMEDIA_IMGREADER_MAX_IMAGES_ACQUIRED
)

func statusName(code int32) string {
	switch code {
	case errUnknown:
		return "ACAMERA_ERROR_UNKNOWN"
	case errInvalidParameter:
		return "ACAMERA_ERROR_INVALID_PARAMETER"
	case errCameraDisconnected:
		return "ACAMERA_ERROR_CAMERA_DISCONNECTED"
	case errNotEnoughMemory:
		return "ACAMERA_ERROR_NOT_ENOUGH_MEMORY"
	case errMetadataNotFound:
		return "ACAMERA_ERROR_METADATA_NOT_FOUND"
	case errCameraDevice:
		return "ACAMERA_ERROR_CAMERA_DEVICE"
	case errCameraService:
		return "ACAMERA_ERROR_CAMERA_SERVICE"
	case errSessionClosed:
		return "ACAMERA_ERROR_SESSION_CLOSED"
	case errInvalidOperation:
		return "ACAMERA_ERROR_INVALID_OPERATION"
	case errStreamConfigureFail:
		return "ACAMERA_ERROR_STREAM_CONFIGURE_FAIL"
	case errCameraInUse:
		return "ACAMERA_ERROR_CAMERA_IN_USE"
	case errMaxCameraInUse:
		return "ACAMERA_ERROR_MAX_CAMERA_IN_USE"
	case errCameraDisabled:
		return "ACAMERA_ERROR_CAMERA_DISABLED"
	case errPermissionDenied:
		return "ACAMERA_ERROR_PERMISSION_DENIED"
	case errUnsupportedOperation:
		return "ACAMERA_ERROR_UNSUPPORTED_OPERATION"
	}
	return fmt.Sprintf("status %d", code)
}

// statusErr turns a camera_status_t into an error wrapping the matching
// library sentinel.
func statusErr(op string, code int32) error {
	name := statusName(code)
	var err error
	switch code {
	case errPermissionDenied:
		err = fmt.Errorf("%w (%s: the app needs android.permission.CAMERA, granted at run time)", camera.ErrPermission, name)
	case errCameraDisabled:
		err = fmt.Errorf("%w (%s: disabled by device policy)", camera.ErrPermission, name)
	case errCameraInUse, errMaxCameraInUse:
		err = fmt.Errorf("%w (%s)", camera.ErrBusy, name)
	case errCameraDisconnected:
		err = fmt.Errorf("%w (%s)", camera.ErrClosed, name)
	case errUnsupportedOperation, errStreamConfigureFail:
		err = fmt.Errorf("%w (%s)", camera.ErrUnsupported, name)
	default:
		err = errors.New(name)
	}
	return &camera.BackendError{Backend: Name, Op: op, Code: int64(code), Err: err}
}

// deviceErr turns an onError code of ACameraDevice_StateCallbacks into the
// error a running stream fails with.
func deviceErr(code int32) error {
	var msg string
	switch code {
	case devErrInUse:
		msg = "camera taken by a higher-priority client"
	case devErrMaxInUse:
		msg = "too many cameras open"
	case devErrDisabled:
		msg = "camera disabled by device policy"
	case devErrDevice:
		msg = "fatal camera device error"
	case devErrService:
		msg = "camera service error"
	default:
		msg = fmt.Sprintf("device error %d", code)
	}
	return &camera.BackendError{Backend: Name, Op: "camera device", Code: int64(code), Err: fmt.Errorf("%w: %s", camera.ErrClosed, msg)}
}

// constEntry is ACameraMetadata_const_entry: uint32 tag; uint8 type;
// uint32 count; a union of const pointers. Go lays the struct out like C
// on every Android ABI (count at 8, data at 16 on 64-bit, 12 on 32-bit).
type constEntry struct {
	Tag   uint32
	Type  uint8
	Count uint32
	Data  unsafe.Pointer
}

// fpsRange is one AE target frame rate range.
type fpsRange struct{ Min, Max int32 }

// streamConfig is one output size of a format.
type streamConfig struct{ W, H int }

// yuvSizes returns the YUV_420_888 output sizes of
// ACAMERA_SCALER_AVAILABLE_STREAM_CONFIGURATIONS (int32 quadruples of
// format, width, height, input flag), duplicates removed, in order.
func yuvSizes(cfg []int32) []streamConfig {
	var out []streamConfig
	seen := map[streamConfig]bool{}
	for i := 0; i+3 < len(cfg); i += 4 {
		if cfg[i] != formatYUV420888 || cfg[i+3] != configOutput {
			continue
		}
		c := streamConfig{int(cfg[i+1]), int(cfg[i+2])}
		if c.W <= 0 || c.H <= 0 || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// minDurations maps YUV_420_888 sizes to their minimum frame duration in
// nanoseconds from ACAMERA_SCALER_AVAILABLE_MIN_FRAME_DURATIONS (int64
// quadruples of format, width, height, duration).
func minDurations(d []int64) map[streamConfig]int64 {
	out := map[streamConfig]int64{}
	for i := 0; i+3 < len(d); i += 4 {
		if d[i] != formatYUV420888 || d[i+3] <= 0 {
			continue
		}
		out[streamConfig{int(d[i+1]), int(d[i+2])}] = d[i+3]
	}
	return out
}

// fpsRanges parses ACAMERA_CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES (int32
// pairs of min, max).
func fpsRanges(r []int32) []fpsRange {
	var out []fpsRange
	for i := 0; i+1 < len(r); i += 2 {
		if r[i] <= 0 || r[i+1] < r[i] {
			continue
		}
		out = append(out, fpsRange{r[i], r[i+1]})
	}
	return out
}

// sizeMaxFPS returns the highest frame rate a size reaches (from its
// minimum frame duration), or 0 when unknown.
func sizeMaxFPS(minDur int64) float64 {
	if minDur <= 0 {
		return 0
	}
	return float64(int(1e9/float64(minDur)*100+0.5)) / 100
}

// ratesFor lists the distinct maximum rates of the ranges a size can run
// (those not above its own maximum), highest first.
func ratesFor(ranges []fpsRange, maxFPS float64) []float64 {
	seen := map[int32]bool{}
	var out []float64
	for _, r := range ranges {
		if maxFPS > 0 && float64(r.Max) > maxFPS+0.5 {
			continue
		}
		if !seen[r.Max] {
			seen[r.Max] = true
			out = append(out, float64(r.Max))
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(out)))
	return out
}

// buildModes turns the characteristics into NV12 modes (frames arrive as
// YUV_420_888 and are packed into NV12 in Go).
func buildModes(configs []int32, durations []int64, ranges []int32) []camera.Mode {
	rs := fpsRanges(ranges)
	durs := minDurations(durations)
	var modes []camera.Mode
	for _, c := range yuvSizes(configs) {
		modes = append(modes, camera.Mode{
			Format: camera.NV12, Width: c.W, Height: c.H,
			FrameRates: ratesFor(rs, sizeMaxFPS(durs[c])),
		})
	}
	camera.SortModes(modes)
	return modes
}

// pickRange chooses the AE target range for a frame rate: a fixed range
// [fps, fps] when offered (steady timing for video), otherwise the range
// whose maximum is fps with the highest minimum, otherwise the narrowest
// range containing fps. ok is false when no range contains fps.
func pickRange(ranges []fpsRange, fps float64) (fpsRange, bool) {
	want := int32(fps + 0.5)
	var best fpsRange
	found := false
	score := func(r fpsRange) int64 {
		switch {
		case r.Min == want && r.Max == want:
			return 0
		case r.Max == want:
			return 1000 - int64(r.Min)
		default:
			return 1_000_000 + int64(r.Max-r.Min)
		}
	}
	for _, r := range ranges {
		if r.Min > want || r.Max < want {
			continue
		}
		if !found || score(r) < score(best) {
			best, found = r, true
		}
	}
	return best, found
}

// position maps ACAMERA_LENS_FACING.
func position(facing int32, ok bool) camera.Position {
	if !ok {
		return camera.PositionUnknown
	}
	switch facing {
	case facingFront:
		return camera.PositionFront
	case facingBack:
		return camera.PositionBack
	case facingExternal:
		return camera.PositionExternal
	}
	return camera.PositionUnknown
}

// deviceName is the name shown for a camera: Android has no human-readable
// camera names in the NDK, so it is built from the facing and the ID.
func deviceName(id string, pos camera.Position) string {
	switch pos {
	case camera.PositionFront:
		return "Front camera " + id
	case camera.PositionBack:
		return "Back camera " + id
	case camera.PositionExternal:
		return "External camera " + id
	}
	return "Camera " + id
}

// markDefault sets Default on the first back camera (or the first camera
// when none faces back) and orders the list by preference.
func markDefault(ds []camera.DeviceInfo) {
	def := -1
	for i, d := range ds {
		if d.Position == camera.PositionBack {
			def = i
			break
		}
	}
	if def < 0 && len(ds) > 0 {
		def = 0
	}
	if def >= 0 {
		ds[def].Default = true
	}
	camera.SortDevices(ds)
}

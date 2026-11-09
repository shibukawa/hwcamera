package camera2

import (
	"errors"
	"reflect"
	"testing"
	"unsafe"

	"github.com/shibukawa/hwcamera/internal/camera"
)

func TestTags(t *testing.T) {
	// Values of camera/NdkCameraMetadataTags.h.
	for _, c := range []struct {
		got, want uint32
	}{
		{tagLensFacing, 0x80005},
		{tagControlAETargetFPSRange, 0x10005},
		{tagControlAEAvailableTargetFPSRange, 0x10014},
		{tagScalerStreamConfigurations, 0xD000A},
		{tagScalerMinFrameDurations, 0xD000B},
		{tagSensorOrientation, 0xE000E},
		{tagInfoSupportedHardwareLevel, 0x150000},
	} {
		if c.got != c.want {
			t.Errorf("tag 0x%x, want 0x%x", c.got, c.want)
		}
	}
}

func TestConstEntryLayout(t *testing.T) {
	var e constEntry
	if unsafe.Sizeof(uintptr(0)) == 8 {
		if unsafe.Offsetof(e.Type) != 4 || unsafe.Offsetof(e.Count) != 8 || unsafe.Offsetof(e.Data) != 16 || unsafe.Sizeof(e) != 24 {
			t.Fatalf("ACameraMetadata_const_entry layout: type %d count %d data %d size %d",
				unsafe.Offsetof(e.Type), unsafe.Offsetof(e.Count), unsafe.Offsetof(e.Data), unsafe.Sizeof(e))
		}
	}
}

func TestBuildModes(t *testing.T) {
	configs := []int32{
		formatJPEG, 4000, 3000, 0,
		formatYUV420888, 1920, 1080, 0,
		formatYUV420888, 1280, 720, 0,
		formatYUV420888, 4000, 3000, 0,
		formatYUV420888, 640, 480, 1, // input (reprocessing): skipped
		formatYUV420888, 1280, 720, 0, // duplicate
	}
	durs := []int64{
		formatYUV420888, 1920, 1080, 33333333,
		formatYUV420888, 1280, 720, 16666666,
		formatYUV420888, 4000, 3000, 50000000,
	}
	ranges := []int32{15, 15, 7, 30, 30, 30, 15, 60, 60, 60}
	modes := buildModes(configs, durs, ranges)
	want := []camera.Mode{
		{Format: camera.NV12, Width: 4000, Height: 3000, FrameRates: []float64{15}},
		{Format: camera.NV12, Width: 1920, Height: 1080, FrameRates: []float64{30, 15}},
		{Format: camera.NV12, Width: 1280, Height: 720, FrameRates: []float64{60, 30, 15}},
	}
	if !reflect.DeepEqual(modes, want) {
		t.Fatalf("modes = %v, want %v", modes, want)
	}
}

func TestPickRange(t *testing.T) {
	rs := fpsRanges([]int32{15, 15, 7, 30, 15, 30, 30, 30, 10, 24, 24, 60})
	for _, c := range []struct {
		fps  float64
		want fpsRange
		ok   bool
	}{
		{30, fpsRange{30, 30}, true},
		{15, fpsRange{15, 15}, true},
		{24, fpsRange{10, 24}, true},
		{29.97, fpsRange{30, 30}, true},
		{45, fpsRange{24, 60}, true},
		{120, fpsRange{}, false},
	} {
		got, ok := pickRange(rs, c.fps)
		if got != c.want || ok != c.ok {
			t.Errorf("pickRange(%v) = %v %v, want %v %v", c.fps, got, ok, c.want, c.ok)
		}
	}
}

func TestDefaultAndErrors(t *testing.T) {
	ds := []camera.DeviceInfo{
		{ID: "1", Position: camera.PositionFront},
		{ID: "0", Position: camera.PositionBack},
		{ID: "5", Position: camera.PositionExternal},
	}
	markDefault(ds)
	if ds[0].ID != "0" || !ds[0].Default || ds[1].Default {
		t.Fatalf("order %v", ds)
	}
	if !errors.Is(statusErr("open", errPermissionDenied), camera.ErrPermission) ||
		!errors.Is(statusErr("open", errCameraInUse), camera.ErrBusy) ||
		!errors.Is(statusErr("open", errMaxCameraInUse), camera.ErrBusy) ||
		!errors.Is(deviceErr(devErrDevice), camera.ErrClosed) {
		t.Fatal("error mapping")
	}
}

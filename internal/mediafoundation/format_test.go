package mediafoundation

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/mediafoundation/sys"
)

func TestSubtypeGUIDs(t *testing.T) {
	// Text forms as printed in mfapi.h / the Media Foundation documentation.
	for _, c := range []struct {
		g    sys.GUID
		want string
	}{
		{sys.MFMediaType_Video, "{73646976-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_NV12, "{3231564E-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_YUY2, "{32595559-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_UYVY, "{59565955-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_I420, "{30323449-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_IYUV, "{56555949-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_MJPG, "{47504A4D-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_RGB32, "{00000016-0000-0010-8000-00AA00389B71}"},
		{sys.MFVideoFormat_ARGB32, "{00000015-0000-0010-8000-00AA00389B71}"},
		{sys.IID_IMFSourceReader, "{70AE66F2-C809-4E4F-8915-BDCB406B7993}"},
		{sys.IID_IMFMediaSource, "{279A808D-AEC7-40C8-9C6B-A6B492C78A66}"},
		{sys.IID_IMF2DBuffer, "{7DC9D5F9-9ED9-44EC-9BBF-0600BB589FBB}"},
		{sys.MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID, "{8AC3587A-4AE7-42D8-99E0-0A6013EEF90F}"},
		{sys.MF_MT_FRAME_RATE, "{C459A2E8-3D2C-4E44-B132-FEE5156C7BB0}"},
	} {
		if got := c.g.String(); got != c.want {
			t.Errorf("GUID %s, want %s", got, c.want)
		}
	}
}

func TestFormatMapping(t *testing.T) {
	for _, f := range []camera.PixelFormat{camera.NV12, camera.YUYV, camera.UYVY, camera.I420, camera.BGRA, camera.MJPEG} {
		g, ok := subtypeFor(f)
		if !ok {
			t.Fatalf("no subtype for %s", f)
		}
		if got := formatFromSubtype(g); got != f {
			t.Errorf("round trip %s -> %s -> %s", f, g, got)
		}
	}
	if formatFromSubtype(sys.MFVideoFormat_ARGB32) != camera.BGRA {
		t.Error("ARGB32 should be BGRA")
	}
	if formatFromSubtype(sys.VideoSubtype(sys.FourCC("H264"))) != camera.Unknown {
		t.Error("H264 should be unknown")
	}
	if _, ok := subtypeFor(camera.RGBA); ok {
		t.Error("RGBA has no Media Foundation subtype")
	}
}

func TestUnpack(t *testing.T) {
	w, h := unpack(0x00000500_000002D0)
	if w != 1280 || h != 720 {
		t.Fatalf("frame size %dx%d", w, h)
	}
	if pack(1920, 1080) != 1920<<32|1080 {
		t.Fatal("pack")
	}
	if r := rateOf(unpack(pack(30000, 1001))); r < 29.97 || r > 29.98 {
		t.Fatalf("rate %v", r)
	}
	if rateOf(30, 0) != 0 {
		t.Fatal("zero denominator")
	}
}

func TestMergeModes(t *testing.T) {
	types := []nativeType{
		{0, camera.YUYV, 640, 480, 30},
		{1, camera.YUYV, 640, 480, 15},
		{2, camera.YUYV, 640, 480, 30}, // duplicate rate
		{3, camera.MJPEG, 1920, 1080, 30},
		{4, camera.YUYV, 1280, 720, 10},
		{5, camera.Unknown, 640, 480, 30},
		{6, camera.NV12, 1280, 720, 0},
	}
	got := mergeModes(types)
	want := []camera.Mode{
		{Format: camera.NV12, Width: 1280, Height: 720},
		{Format: camera.YUYV, Width: 1280, Height: 720, FrameRates: []float64{10}},
		{Format: camera.YUYV, Width: 640, Height: 480, FrameRates: []float64{30, 15}},
		{Format: camera.MJPEG, Width: 1920, Height: 1080, FrameRates: []float64{30}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	nt, ok := pickNative(types, want[2], 15)
	if !ok || nt.index != 1 {
		t.Fatalf("pickNative 15 fps: %+v %v", nt, ok)
	}
	nt, ok = pickNative(types, want[2], 60)
	if !ok || nt.index != 0 {
		t.Fatalf("pickNative fallback: %+v %v", nt, ok)
	}
}

func TestOutputFormat(t *testing.T) {
	for _, c := range []struct {
		native camera.PixelFormat
		cfg    camera.Config
		want   camera.PixelFormat
	}{
		{camera.NV12, camera.Config{Format: camera.NV12}, camera.Unknown},
		{camera.YUYV, camera.Config{Format: camera.NV12}, camera.NV12},
		{camera.MJPEG, camera.Config{Format: camera.NV12}, camera.NV12},
		{camera.MJPEG, camera.Config{Format: camera.Unknown}, camera.NV12},
		{camera.MJPEG, camera.Config{Format: camera.Unknown, Native: camera.MJPEG}, camera.Unknown},
		{camera.MJPEG, camera.Config{Format: camera.MJPEG}, camera.Unknown},
		{camera.MJPEG, camera.Config{Format: camera.BGRA}, camera.BGRA},
		{camera.MJPEG, camera.Config{Format: camera.RGBA}, camera.NV12},
		{camera.YUYV, camera.Config{Format: camera.BGRA}, camera.Unknown},
		{camera.YUYV, camera.Config{Format: camera.Unknown}, camera.Unknown},
	} {
		if got := outputFormat(c.native, c.cfg); got != c.want {
			t.Errorf("outputFormat(%s, %+v) = %s, want %s", c.native, c.cfg, got, c.want)
		}
	}
}

func TestCopyNV12Padded(t *testing.T) {
	// 4x2 picture, pitch 8, Y then CbCr.
	src := []byte{
		1, 2, 3, 4, 0, 0, 0, 0,
		5, 6, 7, 8, 0, 0, 0, 0,
		9, 10, 11, 12, 0, 0, 0, 0,
	}
	ps, err := planesFor(camera.NV12, 8, 2, rect{w: 4, h: 2})
	if err != nil {
		t.Fatal(err)
	}
	planes, strides, err := copyPlanes(src, 0, ps)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(planes[0], []byte{1, 2, 3, 4, 5, 6, 7, 8}) || !bytes.Equal(planes[1], []byte{9, 10, 11, 12}) {
		t.Fatalf("planes %v", planes)
	}
	if !reflect.DeepEqual(strides, []int{4, 4}) {
		t.Fatalf("strides %v", strides)
	}
	lo, hi := extent(ps)
	if lo != 0 || hi != 20 {
		t.Fatalf("extent %d %d", lo, hi)
	}
	// Truncated buffer must fail, not panic.
	if _, _, err := copyPlanes(src[:18], 0, ps); err == nil {
		t.Fatal("expected bounds error")
	}
}

func TestCopyNV12Cropped(t *testing.T) {
	// Coded 4x4 (pitch 4), visible 2x2 at (2,2).
	src := make([]byte, 4*4+4*2)
	for i := range src {
		src[i] = byte(i)
	}
	ps, err := planesFor(camera.NV12, 4, 4, rect{x: 2, y: 2, w: 2, h: 2})
	if err != nil {
		t.Fatal(err)
	}
	planes, _, err := copyPlanes(src, 0, ps)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(planes[0], []byte{10, 11, 14, 15}) || !bytes.Equal(planes[1], []byte{22, 23}) {
		t.Fatalf("planes %v", planes)
	}
}

func TestCopyBGRABottomUp(t *testing.T) {
	// 1x3 BGRA image stored bottom-up: memory rows are display rows 2, 1, 0.
	src := []byte{
		3, 3, 3, 3,
		2, 2, 2, 2,
		1, 1, 1, 1,
	}
	ps, err := planesFor(camera.BGRA, -4, 3, rect{w: 1, h: 3})
	if err != nil {
		t.Fatal(err)
	}
	// Scanline 0 (top row) is the last row in memory.
	planes, strides, err := copyPlanes(src, 8, ps)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(planes[0], []byte{1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3}) || strides[0] != 4 {
		t.Fatalf("planes %v strides %v", planes, strides)
	}
	lo, hi := extent(ps)
	if lo != -8 || hi != 4 {
		t.Fatalf("extent %d %d", lo, hi)
	}
}

func TestCopyI420(t *testing.T) {
	// 2x2 picture, pitch 2: Y(4) U(1) V(1).
	src := []byte{1, 2, 3, 4, 5, 6}
	ps, err := planesFor(camera.I420, 2, 2, rect{w: 2, h: 2})
	if err != nil {
		t.Fatal(err)
	}
	planes, strides, err := copyPlanes(src, 0, ps)
	if err != nil {
		t.Fatal(err)
	}
	if len(planes) != 3 || planes[1][0] != 5 || planes[2][0] != 6 || strides[1] != 1 {
		t.Fatalf("planes %v strides %v", planes, strides)
	}
}

func TestCopyYUYV(t *testing.T) {
	src := []byte{1, 2, 3, 4, 0, 0, 5, 6, 7, 8, 0, 0}
	ps, err := planesFor(camera.YUYV, 6, 2, rect{w: 2, h: 2})
	if err != nil {
		t.Fatal(err)
	}
	planes, _, err := copyPlanes(src, 0, ps)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(planes[0], []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("planes %v", planes)
	}
	if _, err := planesFor(camera.YUYV, 2, 2, rect{w: 2, h: 2}); err == nil {
		t.Fatal("expected pitch error")
	}
	if _, err := planesFor(camera.MJPEG, 2, 2, rect{w: 2, h: 2}); err == nil {
		t.Fatal("expected layout error for MJPEG")
	}
}

func TestInferAllocRows(t *testing.T) {
	if got := inferAllocRows(camera.NV12, 1920, 1080, 1920*1088*3/2); got != 1088 {
		t.Fatalf("got %d", got)
	}
	if got := inferAllocRows(camera.NV12, 1920, 1080, 1920*1080*3/2); got != 1080 {
		t.Fatalf("got %d", got)
	}
	if got := inferAllocRows(camera.NV12, 1920, 1080, 1920*1080*3/2+7); got != 1080 {
		t.Fatalf("got %d", got)
	}
	if got := inferAllocRows(camera.YUYV, 3840, 1080, 1); got != 1080 {
		t.Fatalf("got %d", got)
	}
}

func TestDecodeVideoArea(t *testing.T) {
	b := []byte{0, 0, 4, 0, 0, 0, 2, 0, 0x80, 7, 0, 0, 0x38, 4, 0, 0}
	r, ok := decodeVideoArea(b)
	if !ok || r != (rect{4, 2, 1920, 1080}) {
		t.Fatalf("%+v %v", r, ok)
	}
	if _, ok := decodeVideoArea(b[:8]); ok {
		t.Fatal("short blob")
	}
}

func TestClock(t *testing.T) {
	c := clock{frameDur: 333333}
	ts := int64(5_000_000)
	if d, s := c.next(ts); d != 0 || s != 0 {
		t.Fatalf("first %v %d", d, s)
	}
	if d, s := c.next(ts + 333333); s != 1 || d != 33333300*time.Nanosecond {
		t.Fatalf("second %v %d", d, s)
	}
	// Two frames missing.
	if _, s := c.next(ts + 4*333333); s != 4 {
		t.Fatalf("after gap %d", s)
	}
	// Stream tick without a time gap still counts.
	c.tick()
	if _, s := c.next(ts + 5*333333); s != 6 {
		t.Fatalf("after tick %d", s)
	}
}

func TestHRError(t *testing.T) {
	err := hrError("ActivateObject", sys.E_ACCESSDENIED)
	if !errors.Is(err, camera.ErrPermission) {
		t.Fatalf("%v", err)
	}
	var be *camera.BackendError
	if !errors.As(err, &be) || be.Code != 0x80070005 || be.Backend != Name {
		t.Fatalf("%#v", be)
	}
	for _, hr := range []sys.HRESULT{sys.MF_E_HW_MFT_FAILED_START_STREAMING, sys.MF_E_VIDEO_RECORDING_DEVICE_INVALIDATED, sys.HRESULTFromWin32(32)} {
		if !errors.Is(hrError("ReadSample", hr), camera.ErrBusy) {
			t.Errorf("%s not busy", hr)
		}
	}
	if !errors.Is(hrError("ReadSample", sys.MF_E_SHUTDOWN), camera.ErrClosed) {
		t.Error("shutdown not closed")
	}
	if errors.Is(hrError("x", sys.E_FAIL), camera.ErrBusy) {
		t.Error("E_FAIL is busy")
	}
}

func TestPosition(t *testing.T) {
	tr, fa := true, false
	pld := make([]byte, 20)
	pld[8] = panelBack << 3
	if pldPanel(pld) != panelBack || pldPanel(pld[:4]) != -1 {
		t.Fatal("pldPanel")
	}
	usb := `\\?\usb#vid_046d&pid_085e&mi_00#7&1a2b&0&0000#{e5323777-f976-4f5b-9b55-b94699c46e44}\global`
	for _, c := range []struct {
		link  string
		panel int
		local *bool
		want  camera.Position
	}{
		{usb, -1, nil, camera.PositionExternal},
		{usb, -1, &fa, camera.PositionExternal},
		{usb, -1, &tr, camera.PositionFront},
		{usb, panelBack, &fa, camera.PositionExternal},
		{`\\?\acpi#...`, panelBack, &tr, camera.PositionBack},
		{`\\?\acpi#...`, panelFront, nil, camera.PositionFront},
		{`\\?\swd#vcam`, -1, &tr, camera.PositionUnknown},
	} {
		if got := positionOf(c.link, c.panel, c.local); got != c.want {
			t.Errorf("positionOf(%q, %d) = %s, want %s", c.link, c.panel, got, c.want)
		}
	}
}

func TestHRESULTFromWin32(t *testing.T) {
	if sys.HRESULTFromWin32(32) != sys.HRESULT_SHARING_VIOLATION || sys.HRESULTFromWin32(2404) != sys.HRESULT_DEVICE_IN_USE {
		t.Fatal("HRESULT_FROM_WIN32")
	}
}

package avfoundation

import (
	"sort"
	"testing"

	"github.com/shibukawa/hwcamera/internal/camera"
)

func TestFourCCValues(t *testing.T) {
	cc := func(s string) uint32 {
		return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
	}
	for name, got := range map[string]uint32{
		"420v": fourCC420v, "420f": fourCC420f, "2vuy": fourCC2vuy, "yuvs": fourCCyuvs,
		"BGRA": fourCCBGRA, "jpeg": fourCCjpeg, "dmb1": fourCCdmb1,
	} {
		if want := cc(name); got != want {
			t.Errorf("%s = %#x, want %#x", name, got, want)
		}
	}
}

func TestFormatFromFourCC(t *testing.T) {
	cases := map[uint32]camera.PixelFormat{
		fourCC420v: camera.NV12, fourCC420f: camera.NV12, fourCC2vuy: camera.UYVY,
		fourCCyuvs: camera.YUYV, fourCCBGRA: camera.BGRA, fourCCjpeg: camera.MJPEG,
		fourCCdmb1: camera.MJPEG, 0x61766331: camera.Unknown,
	}
	for cc, want := range cases {
		if got := formatFromFourCC(cc); got != want {
			t.Errorf("%#x: got %s want %s", cc, got, want)
		}
	}
}

func TestOutputFourCC(t *testing.T) {
	for _, f := range []camera.PixelFormat{camera.Unknown, camera.NV12, camera.I420, camera.YUYV, camera.MJPEG} {
		if cc, got := outputFourCC(f); cc != fourCC420v || got != camera.NV12 {
			t.Errorf("%s: got %#x %s", f, cc, got)
		}
	}
	for _, f := range []camera.PixelFormat{camera.RGBA, camera.BGRA} {
		if cc, got := outputFourCC(f); cc != fourCCBGRA || got != camera.BGRA {
			t.Errorf("%s: got %#x %s", f, cc, got)
		}
	}
}

func TestRatesOf(t *testing.T) {
	got := ratesOf([]frameRange{{1, 30.00003}, {60, 60}})
	want := []float64{30.00003, 1, 60}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	got = ratesOf([]frameRange{{5, 60}})
	if len(got) != 3 || got[0] != 60 || got[1] != 30 || got[2] != 5 {
		t.Fatalf("got %v", got)
	}
}

func TestMergeModes(t *testing.T) {
	in := []camera.Mode{
		{Format: camera.NV12, Width: 640, Height: 480, FrameRates: []float64{30}},
		{Format: camera.NV12, Width: 1280, Height: 720, FrameRates: []float64{30, 15}},
		{Format: camera.NV12, Width: 1280, Height: 720, FrameRates: []float64{60, 30.001}},
		{Format: camera.Unknown, Width: 1280, Height: 720},
		{Format: camera.MJPEG, Width: 1920, Height: 1080, FrameRates: []float64{30}},
	}
	out := mergeModes(in)
	if len(out) != 3 {
		t.Fatalf("got %v", out)
	}
	if out[0].Width != 1280 || out[0].Format != camera.NV12 {
		t.Fatalf("order: %v", out)
	}
	if fr := out[0].FrameRates; len(fr) != 3 || fr[0] != 60 || fr[1] != 30 || fr[2] != 15 {
		t.Fatalf("rates: %v", fr)
	}
	if out[2].Format != camera.MJPEG {
		t.Fatalf("order: %v", out)
	}
}

func TestDurationFor(t *testing.T) {
	r := frameRange{MinRate: 2, MaxRate: 29.97002997}
	minD := cmTime{Value: 1001, Timescale: 30000, Flags: cmTimeFlagsValid}
	maxD := cmTime{Value: 1, Timescale: 2, Flags: cmTimeFlagsValid}
	if d := durationFor(29.97002997, r, minD, maxD); d != minD {
		t.Errorf("max edge: %+v", d)
	}
	if d := durationFor(2, r, minD, maxD); d != maxD {
		t.Errorf("min edge: %+v", d)
	}
	if d := durationFor(15, r, minD, maxD); d.Value != 1 || d.Timescale != 15 || !d.valid() {
		t.Errorf("15: %+v", d)
	}
	if d := durationFor(12.5, r, minD, maxD); d.Value != 1000 || d.Timescale != 12500 {
		t.Errorf("12.5: %+v", d)
	}
	if !rangeCovers(r, 29.97) || rangeCovers(r, 60) {
		t.Error("rangeCovers")
	}
}

func TestNanos(t *testing.T) {
	a := cmTime{Value: 1_234_567_890_123_456, Timescale: 1_000_000_000, Flags: cmTimeFlagsValid}
	if got := a.nanos(); got != 1_234_567_890_123_456 {
		t.Errorf("host clock: %d", got)
	}
	b := cmTime{Value: 3_000_001, Timescale: 30000, Flags: cmTimeFlagsValid}
	if got, want := b.nanos(), int64(100_000_033_333); got != want {
		t.Errorf("30000 timescale: %d want %d", got, want)
	}
	if (cmTime{Value: 5, Timescale: 1}).nanos() != 0 {
		t.Error("invalid time must be 0")
	}
}

func TestPositionFor(t *testing.T) {
	cases := []struct {
		pos  int
		kind deviceKind
		want camera.Position
	}{
		{0, kindBuiltIn, camera.PositionFront},
		{0, kindExternal, camera.PositionExternal},
		{1, kindExternal, camera.PositionExternal},
		{1, kindUnknown, camera.PositionBack},
		{2, kindUnknown, camera.PositionFront},
		{0, kindUnknown, camera.PositionUnknown},
	}
	for _, c := range cases {
		if got := positionFor(c.pos, c.kind); got != c.want {
			t.Errorf("%d/%d: got %s want %s", c.pos, c.kind, got, c.want)
		}
	}
}

func TestTransportVirtual(t *testing.T) {
	if transportVirtual != 0x76697274 {
		t.Fatalf("transportVirtual = %#x", transportVirtual)
	}
}

func TestIOSDeviceOrder(t *testing.T) {
	type cam struct {
		name string
		t    iosType
		pos  int
	}
	in := []cam{
		{"external", iosTypeExternal, 0},
		{"tele", iosTypeTelephoto, avPosBack},
		{"front wide", iosTypeWide, avPosFront},
		{"ultra wide", iosTypeUltraWide, avPosBack},
		{"back wide", iosTypeWide, avPosBack},
		{"front ultra wide", iosTypeUltraWide, avPosFront},
	}
	sort.SliceStable(in, func(i, j int) bool {
		return iosDeviceOrder(in[i].t, in[i].pos) < iosDeviceOrder(in[j].t, in[j].pos)
	})
	want := []string{"back wide", "front wide", "ultra wide", "front ultra wide", "tele", "external"}
	for i, c := range in {
		if c.name != want[i] {
			t.Fatalf("order[%d] = %s, want %s (all: %v)", i, c.name, want[i], in)
		}
	}
}

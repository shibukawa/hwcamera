package camera

import (
	"errors"
	"testing"
)

var uvc = []Mode{
	{Format: MJPEG, Width: 1920, Height: 1080, FrameRates: []float64{30}},
	{Format: MJPEG, Width: 1280, Height: 720, FrameRates: []float64{30}},
	{Format: YUYV, Width: 1920, Height: 1080, FrameRates: []float64{5}},
	{Format: YUYV, Width: 1280, Height: 720, FrameRates: []float64{10}},
	{Format: YUYV, Width: 640, Height: 480, FrameRates: []float64{30, 15}},
}

func TestChooseMode(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  Config
		want Mode
	}{
		// 720p30 is only reachable as MJPEG on a USB 2 camera.
		{"default", Config{Format: NV12}, uvc[1]},
		{"vga", Config{Width: 640, Height: 480, Format: NV12}, uvc[4]},
		{"720p at 10fps prefers raw", Config{Width: 1280, Height: 720, FrameRate: 10, Format: NV12}, uvc[3]},
		{"forced yuyv", Config{Width: 1920, Height: 1080, Native: YUYV}, uvc[2]},
		{"odd size rounds up", Config{Width: 800, Height: 600, Format: NV12}, uvc[1]},
		{"too large takes largest", Config{Width: 4000, Height: 3000, Format: NV12}, uvc[0]},
	} {
		got, err := ChooseMode(uvc, c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != c.want.String() {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	if _, err := ChooseMode(uvc, Config{Native: NV12}); err == nil {
		t.Error("missing native format accepted")
	}
}

func TestPickFrameRate(t *testing.T) {
	m := Mode{FrameRates: []float64{60, 30, 15}}
	if r := PickFrameRate(m, 0); r != 30 {
		t.Errorf("default = %v", r)
	}
	if r := PickFrameRate(m, 25); r != 30 {
		t.Errorf("25 -> %v, want 30", r)
	}
	if r := PickFrameRate(m, 100); r != 60 {
		t.Errorf("100 -> %v, want 60", r)
	}
}

func TestSortDevices(t *testing.T) {
	ds := []DeviceInfo{
		{ID: "vr", Position: PositionExternal, Virtual: true},
		{ID: "usb", Position: PositionExternal},
		{ID: "virtual-default", Default: true, Virtual: true},
		{ID: "lid", Position: PositionFront},
		{ID: "iphone", Position: PositionExternal, Default: true},
	}
	SortDevices(ds)
	var got []string
	for _, d := range ds {
		got = append(got, d.ID)
	}
	want := []string{"iphone", "lid", "usb", "virtual-default", "vr"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

func TestOpaqueAlpha(t *testing.T) {
	f := &Frame{Width: 2, Height: 2, Format: BGRA, Planes: [][]byte{make([]byte, 24)}, Strides: []int{12}}
	f.Planes[0][0] = 7
	OpaqueAlpha(f)
	p := f.Planes[0]
	if p[3] != 255 || p[7] != 255 || p[15] != 255 || p[19] != 255 || p[0] != 7 || p[11] != 0 {
		t.Fatalf("got %v", p)
	}
}

func TestKind(t *testing.T) {
	d := DeviceInfo{Position: PositionBack, Lens: LensUltraWide}
	if got := d.Kind(); got != KindBack|KindUltraWide|KindPhysical {
		t.Fatalf("Kind = %s", got)
	}
	for k, want := range map[Kind]bool{
		0: true, KindBack: true, KindBack | KindUltraWide: true, KindPhysical: true,
		KindFront: false, KindBack | KindWide: false, KindVirtual: false,
	} {
		if d.Is(k) != want {
			t.Errorf("Is(%s) = %v", k, !want)
		}
	}
	if s := (KindFront | KindTelephoto | KindVirtual).String(); s != "front|telephoto|virtual" {
		t.Errorf("String = %q", s)
	}
}

func TestParseKind(t *testing.T) {
	for in, want := range map[string]Kind{
		"back":               KindBack,
		"Rear":               KindBack,
		"back+ultrawide":     KindBack | KindUltraWide,
		"front|ultra-wide":   KindFront | KindUltraWide,
		"back, tele":         KindBack | KindTelephoto,
		"external physical":  KindExternal | KindPhysical,
		"front|back|virtual": KindFront | KindBack | KindVirtual,
		"back+low":           KindBack | KindLow,
		"HIGH,16:9":          KindHigh | KindR16_9,
		"medium 4x3":         KindMid | KindR4_3,
		"r16_9+portrait":     KindR16_9 | KindPortrait,
		"square":             KindSquare,
	} {
		got, err := ParseKind(in)
		if err != nil || got != want {
			t.Errorf("ParseKind(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/dev/video0", "back+fisheye", "0"} {
		if _, err := ParseKind(in); err == nil {
			t.Errorf("ParseKind(%q): no error", in)
		}
	}
}

func TestModeKind(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want Kind
	}{
		{640, 480, KindLow | KindLandscape | KindR4_3},
		{640, 360, KindLow | KindLandscape | KindR16_9},
		{854, 480, KindLow | KindLandscape | KindR16_9},
		{800, 600, KindLow | KindLandscape | KindR4_3},
		{1280, 720, KindMid | KindLandscape | KindR16_9},
		{1280, 960, KindMid | KindLandscape | KindR4_3},
		{1280, 800, KindMid | KindLandscape},
		{1920, 1080, KindHigh | KindLandscape | KindR16_9},
		{4032, 3024, KindHigh | KindLandscape | KindR4_3},
		{1080, 1920, KindHigh | KindPortrait | KindR16_9},
		{480, 480, KindLow | KindSquare},
	} {
		if got := (Mode{Width: c.w, Height: c.h}).Kind(); got != c.want {
			t.Errorf("%dx%d: %s, want %s", c.w, c.h, got, c.want)
		}
	}
}

func TestChooseModeKind(t *testing.T) {
	modes := []Mode{
		{Format: NV12, Width: 3840, Height: 2160, FrameRates: []float64{15}},
		{Format: NV12, Width: 1920, Height: 1080, FrameRates: []float64{30}},
		{Format: NV12, Width: 1440, Height: 1080, FrameRates: []float64{30}},
		{Format: NV12, Width: 1280, Height: 960, FrameRates: []float64{30}},
		{Format: NV12, Width: 1280, Height: 720, FrameRates: []float64{30}},
		{Format: NV12, Width: 640, Height: 480, FrameRates: []float64{30}},
		{Format: NV12, Width: 640, Height: 360, FrameRates: []float64{30}},
		{Format: NV12, Width: 320, Height: 240, FrameRates: []float64{30}},
	}
	for _, c := range []struct {
		kind  Kind
		w, h  int // asked size
		fps   float64
		wantW int
		wantH int
	}{
		{KindLow, 0, 0, 0, 640, 480},
		{KindLow | KindR16_9, 0, 0, 0, 640, 360},
		{KindMid, 0, 0, 0, 1280, 720},
		{KindR4_3, 0, 0, 0, 1280, 960},
		{KindHigh, 0, 0, 0, 1920, 1080},            // 4K only reaches 15 fps
		{KindHigh, 0, 0, 15, 3840, 2160},           // which is enough here
		{KindHigh | KindR4_3, 0, 0, 0, 1440, 1080}, // 1440x1080 is 4:3
		{KindLow, 320, 240, 0, 320, 240},           // a size wins inside the class
		{KindHigh | KindPhysical, 0, 0, 0, 1920, 1080},
	} {
		m, err := ChooseMode(modes, Config{Kind: c.kind, Width: c.w, Height: c.h, FrameRate: c.fps})
		if err != nil {
			t.Errorf("%s: %v", c.kind, err)
			continue
		}
		if m.Width != c.wantW || m.Height != c.wantH {
			t.Errorf("%s %dx%d@%v: %dx%d, want %dx%d", c.kind, c.w, c.h, c.fps, m.Width, m.Height, c.wantW, c.wantH)
		}
	}
	for _, k := range []Kind{KindSquare, KindPortrait, KindLow | KindMid, KindMid | KindR4_3 | KindR16_9} {
		if _, err := ChooseMode(modes, Config{Kind: k}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", k, err)
		}
	}
	d := DeviceInfo{Position: PositionBack, Modes: modes}
	if !d.Is(KindBack|KindHigh|KindR16_9) || d.Is(KindBack|KindSquare) || d.Is(KindFront|KindLow) {
		t.Error("DeviceInfo.Is with mode traits")
	}
}

// The FaceTime HD camera of a MacBook (M2) lists square and portrait
// modes for Center Stage among the usual ones.
var facetime = []Mode{
	{Format: NV12, Width: 1552, Height: 1552, FrameRates: []float64{30, 15}},
	{Format: NV12, Width: 1760, Height: 1328, FrameRates: []float64{30, 15}},
	{Format: NV12, Width: 1328, Height: 1760, FrameRates: []float64{30, 15}},
	{Format: NV12, Width: 1920, Height: 1080, FrameRates: []float64{30, 15}},
	{Format: NV12, Width: 1080, Height: 1920, FrameRates: []float64{30, 15}},
	{Format: NV12, Width: 1280, Height: 720, FrameRates: []float64{30, 15}},
	{Format: NV12, Width: 640, Height: 480, FrameRates: []float64{30, 15}},
}

func TestChooseModeLandscapeByDefault(t *testing.T) {
	for _, c := range []struct {
		kind         Kind
		w, h         int
		wantW, wantH int
	}{
		{0, 0, 0, 1280, 720},
		{KindHigh, 0, 0, 1760, 1328}, // not the larger 1552x1552
		{KindHigh | KindR16_9, 0, 0, 1920, 1080},
		{KindLow, 0, 0, 640, 480},
		{0, 1600, 1600, 1552, 1552}, // a square size asks for square
		{KindSquare, 0, 0, 1552, 1552},
		{KindPortrait, 0, 0, 1328, 1760},
		{KindPortrait | KindR16_9, 0, 0, 1080, 1920},
		{KindHigh | KindPortrait, 0, 0, 1328, 1760},
		{0, 1080, 1920, 1080, 1920}, // a portrait size asks for portrait
		{0, 1552, 1552, 1552, 1552},
		{0, 1600, 0, 1760, 1328}, // a width alone stays landscape
	} {
		m, err := ChooseMode(facetime, Config{Kind: c.kind, Width: c.w, Height: c.h})
		if err != nil {
			t.Errorf("%s %dx%d: %v", c.kind, c.w, c.h, err)
			continue
		}
		if m.Width != c.wantW || m.Height != c.wantH {
			t.Errorf("%s %dx%d: %dx%d, want %dx%d", c.kind, c.w, c.h, m.Width, m.Height, c.wantW, c.wantH)
		}
	}
	// A camera with portrait modes only still opens.
	m, err := ChooseMode([]Mode{{Format: NV12, Width: 720, Height: 1280}}, Config{})
	if err != nil || m.Width != 720 {
		t.Errorf("portrait-only camera: %v %v", m, err)
	}
}

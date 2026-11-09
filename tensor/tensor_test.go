package tensor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/pixconv"
)

func solid(w, h int, r, g, b byte) *camera.Frame {
	f := pixconv.NewFrame(camera.RGBA, w, h)
	for i := 0; i < len(f.Planes[0]); i += 4 {
		f.Planes[0][i], f.Planes[0][i+1], f.Planes[0][i+2], f.Planes[0][i+3] = r, g, b, 255
	}
	return f
}

func TestF16(t *testing.T) {
	for _, c := range []struct {
		f float32
		h uint16
	}{{0, 0}, {1, 0x3c00}, {-2, 0xc000}, {0.5, 0x3800}, {65504, 0x7bff}, {1e6, 0x7c00}, {5.96e-8, 0x0001}, {0.333251953125, 0x3555}} {
		if got := F16(c.f); got != c.h {
			t.Errorf("F16(%g) = %#04x, want %#04x", c.f, got, c.h)
		}
	}
	for h := 0; h < 0x7c00; h++ {
		if F16(F16ToF32(uint16(h))) != uint16(h) {
			t.Fatalf("round trip of %#04x", h)
		}
	}
	if !math.IsNaN(float64(F16ToF32(F16(float32(math.NaN()))))) {
		t.Fatal("NaN")
	}
}

func TestLayoutsAndNormalisation(t *testing.T) {
	f := solid(64, 32, 255, 0, 51)
	s := Spec{Width: 8, Height: 4, Layout: NCHW, DType: Float32, Mean: [3]float32{0, 0, 0.1}, Std: [3]float32{1, 1, 0.5}}
	dst := make([]byte, s.Bytes())
	if _, err := Fill(dst, f, s); err != nil {
		t.Fatal(err)
	}
	at := func(i int) float32 { return math.Float32frombits(binary.NativeEndian.Uint32(dst[4*i:])) }
	n := 8 * 4
	if at(0) != 1 || at(n) != 0 || math.Abs(float64(at(2*n))-(0.2-0.1)/0.5) > 1e-6 {
		t.Fatalf("NCHW values %v %v %v", at(0), at(n), at(2*n))
	}
	s.Layout, s.Order, s.DType = NHWC, BGR, Uint8
	dst = make([]byte, s.Bytes())
	Fill(dst, f, s)
	if dst[0] != 51 || dst[1] != 0 || dst[2] != 255 {
		t.Fatalf("NHWC BGR uint8 = %v", dst[:3])
	}
}

func TestLetterbox(t *testing.T) {
	f := solid(200, 100, 200, 200, 200)
	s := Spec{Width: 100, Height: 100, Layout: NHWC, DType: Uint8, Resize: Letterbox, Pad: [3]uint8{114, 114, 114}}
	dst := make([]byte, s.Bytes())
	tr, err := Fill(dst, f, s)
	if err != nil {
		t.Fatal(err)
	}
	if dst[0] != 114 || dst[(50*100+50)*3] != 200 || dst[(99*100+99)*3] != 114 {
		t.Fatal("letterbox bands wrong")
	}
	x, y := tr.ToFrame(50, 50)
	if math.Abs(x-100) > 1e-9 || math.Abs(y-50) > 1e-9 {
		t.Fatalf("centre maps to %v,%v", x, y)
	}
	x, y = tr.ToFrame(0, 25)
	if math.Abs(x) > 1e-9 || math.Abs(y) > 1e-9 {
		t.Fatalf("corner maps to %v,%v", x, y)
	}
}

func TestCenterCropAndYUVInput(t *testing.T) {
	src := solid(160, 120, 10, 200, 30)
	c, _ := pixconv.NewConverter(camera.RGBA, camera.NV12)
	nv, _ := c.Convert(src)
	s := Spec{Width: 32, Height: 32, Layout: NHWC, DType: Uint8, Resize: CenterCrop}
	dst := make([]byte, s.Bytes())
	tr, err := Fill(dst, nv, s)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []byte{10, 200, 30} {
		if d := int(dst[i]) - int(want); d < -4 || d > 4 {
			t.Errorf("channel %d = %d, want ~%d", i, dst[i], want)
		}
	}
	x, _ := tr.ToFrame(0, 0)
	if math.Abs(x-20) > 1e-9 {
		t.Fatalf("crop origin x = %v, want 20", x)
	}
}

func BenchmarkFill1080pTo640F16(b *testing.B) {
	f := pixconv.NewFrame(camera.NV12, 1920, 1080)
	s := Spec{Width: 640, Height: 640, DType: Float16, Resize: Letterbox}
	dst := make([]byte, s.Bytes())
	for b.Loop() {
		Fill(dst, f, s)
	}
}

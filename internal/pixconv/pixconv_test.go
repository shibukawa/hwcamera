package pixconv

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// testRGBA is a smooth gradient: chroma subsampling loses little on it, so
// round trips can be compared with a tight tolerance.
func testRGBA(w, h int) *camera.Frame {
	f := NewFrame(camera.RGBA, w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := f.Planes[0][y*f.Strides[0]+4*x:]
			p[0], p[1], p[2], p[3] = byte(40+x*160/w), byte(60+y*120/h), byte(200-x*100/w), 255
		}
	}
	return f
}

func maxDiff(a, b *camera.Frame) int {
	d := 0
	for y := 0; y < a.Height; y++ {
		for x := 0; x < 4*a.Width; x++ {
			if x%4 == 3 {
				continue
			}
			v := int(a.Planes[0][y*a.Strides[0]+x]) - int(b.Planes[0][y*b.Strides[0]+x])
			if v < 0 {
				v = -v
			}
			d = max(d, v)
		}
	}
	return d
}

func TestRoundTrips(t *testing.T) {
	for _, size := range [][2]int{{64, 48}, {33, 17}} {
		src := testRGBA(size[0], size[1])
		for _, mid := range []camera.PixelFormat{camera.NV12, camera.I420, camera.YUYV, camera.UYVY, camera.BGRA} {
			a, _ := NewConverter(camera.RGBA, mid)
			b, _ := NewConverter(mid, camera.RGBA)
			m, err := a.Convert(src)
			if err != nil {
				t.Fatal(err)
			}
			back, err := b.Convert(m)
			if err != nil {
				t.Fatal(err)
			}
			if d := maxDiff(src, back); d > 6 {
				t.Errorf("%dx%d via %s: max diff %d", size[0], size[1], mid, d)
			}
			m.Release()
			back.Release()
		}
	}
}

func TestYUVChains(t *testing.T) {
	src := testRGBA(32, 32)
	nv12, _ := NewConverter(camera.RGBA, camera.NV12)
	n, _ := nv12.Convert(src)
	for _, to := range []camera.PixelFormat{camera.I420, camera.YUYV, camera.UYVY} {
		c1, _ := NewConverter(camera.NV12, to)
		c2, _ := NewConverter(to, camera.NV12)
		x, _ := c1.Convert(n)
		y, _ := c2.Convert(x)
		if !bytes.Equal(y.Planes[0], n.Planes[0]) || !bytes.Equal(y.Planes[1], n.Planes[1]) {
			t.Errorf("NV12 -> %s -> NV12 not lossless", to)
		}
	}
}

func TestColorPoints(t *testing.T) {
	for _, c := range []struct{ r, g, b, y byte }{{0, 0, 0, 16}, {255, 255, 255, 235}} {
		y, cb, cr := RGBToYCbCr(c.r, c.g, c.b)
		if y != c.y || cb != 128 || cr != 128 {
			t.Errorf("RGBToYCbCr(%d,%d,%d) = %d,%d,%d", c.r, c.g, c.b, y, cb, cr)
		}
		r, g, b := YCbCrToRGB(y, cb, cr)
		if r != c.r || g != c.g || b != c.b {
			t.Errorf("YCbCrToRGB(%d) = %d,%d,%d", y, r, g, b)
		}
	}
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{byte(x * 4), byte(y * 4), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stripDHT removes every DHT segment, as cameras following the AVI1
// convention send their frames.
func stripDHT(data []byte) []byte {
	out := append([]byte(nil), data[:2]...)
	i := 2
	for i+4 <= len(data) {
		m := data[i+1]
		n := int(data[i+2])<<8 | int(data[i+3])
		if m == 0xda {
			return append(out, data[i:]...)
		}
		if m != 0xc4 {
			out = append(out, data[i:i+2+n]...)
		}
		i += 2 + n
	}
	return out
}

func TestMJPEGWithoutHuffmanTables(t *testing.T) {
	full := encodeJPEG(t, 64, 48)
	bare := stripDHT(full)
	if bytes.Contains(bare, []byte{0xff, 0xc4}) {
		t.Fatal("DHT still present")
	}
	if _, err := jpeg.Decode(bytes.NewReader(bare)); err == nil {
		t.Fatal("image/jpeg decoded a frame without tables; test is moot")
	}
	src := &camera.Frame{Width: 64, Height: 48, Format: camera.MJPEG, Planes: [][]byte{bare}, Strides: []int{0}}
	ref := &camera.Frame{Width: 64, Height: 48, Format: camera.MJPEG, Planes: [][]byte{full}, Strides: []int{0}}
	for _, to := range []camera.PixelFormat{camera.NV12, camera.RGBA, camera.BGRA, camera.YUYV} {
		c, _ := NewConverter(camera.MJPEG, to)
		a, err := c.Convert(src)
		if err != nil {
			t.Fatalf("%s: %v", to, err)
		}
		b, err := c.Convert(ref)
		if err != nil {
			t.Fatal(err)
		}
		for p := range a.Planes {
			if !bytes.Equal(a.Planes[p], b.Planes[p]) {
				t.Errorf("%s: frame without tables decodes differently", to)
			}
		}
	}
}

func BenchmarkYUYVToNV12_1080p(b *testing.B) {
	src := NewFrame(camera.YUYV, 1920, 1080)
	c, _ := NewConverter(camera.YUYV, camera.NV12)
	for b.Loop() {
		f, _ := c.Convert(src)
		f.Release()
	}
}

func BenchmarkNV12ToRGBA_1080p(b *testing.B) {
	src := NewFrame(camera.NV12, 1920, 1080)
	c, _ := NewConverter(camera.NV12, camera.RGBA)
	for b.Loop() {
		f, _ := c.Convert(src)
		f.Release()
	}
}

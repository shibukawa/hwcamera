package camera2

import (
	"bytes"
	"testing"
)

// image builds a YUV_420_888 image of w x h with distinct sample values:
// Y(x,y) = x+16y, U(x,y) = 100+x+8y, V(x,y) = 200-x-8y (chroma
// coordinates), laid out per layout: "planar", "nv12" or "nv21", with row
// strides padded by pad bytes.
func image(w, h, pad int, layout string) (y, u, v plane, inter bool) {
	ys := w + pad
	yd := make([]byte, ys*h)
	for r := 0; r < h; r++ {
		for x := 0; x < w; x++ {
			yd[r*ys+x] = byte(x + 16*r)
		}
	}
	y = plane{yd, ys, 1}
	cw, ch := w/2, h/2
	uval := func(x, r int) byte { return byte(100 + x + 8*r) }
	vval := func(x, r int) byte { return byte(200 - x - 8*r) }
	switch layout {
	case "planar":
		cs := cw + pad
		ud, vd := make([]byte, cs*(ch-1)+cw), make([]byte, cs*(ch-1)+cw)
		for r := 0; r < ch; r++ {
			for x := 0; x < cw; x++ {
				ud[r*cs+x] = uval(x, r)
				vd[r*cs+x] = vval(x, r)
			}
		}
		return y, plane{ud, cs, 1}, plane{vd, cs, 1}, false
	default:
		cs := 2*cw + pad
		buf := make([]byte, cs*ch)
		first, second := uval, vval
		if layout == "nv21" {
			first, second = vval, uval
		}
		for r := 0; r < ch; r++ {
			for x := 0; x < cw; x++ {
				buf[r*cs+2*x] = first(x, r)
				buf[r*cs+2*x+1] = second(x, r)
			}
		}
		n := cs*(ch-1) + 2*cw - 1 // the length the NDK reports
		a, b := plane{buf[:n], cs, 2}, plane{buf[1 : 1+n], cs, 2}
		if layout == "nv21" {
			return y, b, a, false
		}
		return y, a, b, true
	}
}

func want(w, h, x0, y0 int) []byte {
	out := make([]byte, nv12Size(w, h))
	for r := 0; r < h; r++ {
		for x := 0; x < w; x++ {
			out[r*w+x] = byte(x0 + x + 16*(y0+r))
		}
	}
	for r := 0; r < h/2; r++ {
		for x := 0; x < w/2; x++ {
			cx, cy := x0/2+x, y0/2+r
			out[w*h+r*w+2*x] = byte(100 + cx + 8*cy)
			out[w*h+r*w+2*x+1] = byte(200 - cx - 8*cy)
		}
	}
	return out
}

func TestToNV12(t *testing.T) {
	for _, layout := range []string{"planar", "nv12", "nv21"} {
		for _, pad := range []int{0, 12} {
			const W, H = 16, 8
			y, u, v, inter := image(W, H, pad, layout)
			dst := make([]byte, nv12Size(W, H))
			toNV12(dst, 0, 0, W, H, y, u, v, inter)
			if !bytes.Equal(dst, want(W, H, 0, 0)) {
				t.Errorf("%s pad %d: wrong output", layout, pad)
			}
			// A crop window.
			dst = make([]byte, nv12Size(8, 4))
			toNV12(dst, 4, 2, 8, 4, y, u, v, inter)
			if !bytes.Equal(dst, want(8, 4, 4, 2)) {
				t.Errorf("%s pad %d crop: wrong output", layout, pad)
			}
		}
	}
}

func TestCoversAndCrop(t *testing.T) {
	y, u, v, _ := image(16, 8, 0, "nv12")
	if !y.covers(0, 0, 16, 8) || !u.covers(0, 0, 8, 4) || !v.covers(0, 0, 8, 4) || y.covers(0, 0, 16, 9) || u.covers(1, 0, 8, 4) {
		t.Fatal("covers")
	}
	for _, c := range []struct {
		l, tp, r, b int
		ok          bool
		want        [4]int
	}{
		{0, 0, 0, 0, false, [4]int{0, 0, 1920, 1080}},
		{0, 0, 1920, 1080, true, [4]int{0, 0, 1920, 1080}},
		{0, 4, 1920, 1084, true, [4]int{0, 0, 1920, 1080}}, // out of bounds
		{1, 1, 1919, 1079, true, [4]int{2, 2, 1916, 1076}},
	} {
		x0, y0, w, h := cropWindow(1920, 1080, c.l, c.tp, c.r, c.b, c.ok)
		if [4]int{x0, y0, w, h} != c.want {
			t.Errorf("crop %v = %v", c, [4]int{x0, y0, w, h})
		}
	}
}

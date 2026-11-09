// Package tensor turns camera frames into the input tensors of image
// models: resize (stretch, letterbox or centre crop), colour order, layout
// (NCHW or NHWC), normalisation and element type (float32, float16, uint8,
// int8).
//
// It writes into a caller-supplied byte slice, so the tensor can go straight
// into the input buffer of a runtime without a copy, for example a
// github.com/shibukawa/hwnpu Buffer:
//
//	spec := tensor.Spec{Width: 224, Height: 224, Layout: tensor.NCHW, DType: tensor.Float16,
//		Mean: tensor.ImageNetMean, Std: tensor.ImageNetStd}
//	buf, _ := dev.AllocBuffer(spec.Bytes())
//	tr, err := tensor.Fill(buf.Bytes(), frame, spec)
//	// ... run the model; map its boxes back with tr.ToFrame(x, y)
package tensor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"

	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/pixconv"
)

// Layout is the order of the tensor's axes. The batch axis is always 1.
type Layout int

const (
	NCHW Layout = iota // planar: all R, then all G, then all B (PyTorch, ONNX, Core ML, OpenVINO)
	NHWC               // interleaved: R G B per pixel (TensorFlow Lite)
)

// DType is the element type.
type DType int

const (
	Float32 DType = iota
	Float16
	Uint8 // raw 0..255, Mean and Std ignored
	Int8  // value - 128, Mean and Std ignored (quantized models with zero point 128)
)

// Size returns the bytes per element.
func (d DType) Size() int {
	switch d {
	case Float32:
		return 4
	case Float16:
		return 2
	}
	return 1
}

// ColorOrder is the channel order.
type ColorOrder int

const (
	RGB  ColorOrder = iota
	BGR             // OpenCV-trained models
	Gray            // one channel, BT.601 luma
)

// Resize says how the frame is fitted into the tensor.
type Resize int

const (
	// Stretch scales width and height independently.
	Stretch Resize = iota
	// Letterbox keeps the aspect ratio and pads (YOLO style). The pad
	// colour is Spec.Pad.
	Letterbox
	// CenterCrop keeps the aspect ratio and cuts the longer side.
	CenterCrop
)

var (
	// ImageNetMean and ImageNetStd are the usual normalisation of models
	// trained on ImageNet, for values scaled to 0..1.
	ImageNetMean = [3]float32{0.485, 0.456, 0.406}
	ImageNetStd  = [3]float32{0.229, 0.224, 0.225}
	// Symmetric maps 0..1 to -1..1 (Mean 0.5, Std 0.5), as many mobile models expect.
	SymmetricMean = [3]float32{0.5, 0.5, 0.5}
	SymmetricStd  = [3]float32{0.5, 0.5, 0.5}
)

// Spec describes the tensor.
type Spec struct {
	Width, Height int
	Layout        Layout
	DType         DType
	Order         ColorOrder
	Resize        Resize
	// Float element = (pixel/255*Scale - Mean[c]) / Std[c]. Scale 0 means
	// 1, a zero Std means 1, so the zero Spec gives values in 0..1. Use
	// Scale 255 with zero Mean and Std for models that take 0..255 floats. Mean and Std are given in R, G, B order whatever
	// the output Order.
	Scale float32
	Mean  [3]float32
	Std   [3]float32
	// Pad is the letterbox colour (R, G, B). Zero is black; YOLO models use 114.
	Pad [3]uint8
	// BigEndian writes float elements big-endian. False (the default) is
	// the host's order, which is what CPU, GPU and NPU runtimes expect.
	BigEndian bool
}

// Channels returns 1 for Gray, 3 otherwise.
func (s Spec) Channels() int {
	if s.Order == Gray {
		return 1
	}
	return 3
}

// Elements returns the number of elements.
func (s Spec) Elements() int { return s.Width * s.Height * s.Channels() }

// Bytes returns the size of the tensor in bytes.
func (s Spec) Bytes() int { return s.Elements() * s.DType.Size() }

// Shape returns the tensor shape with batch 1.
func (s Spec) Shape() []int {
	if s.Layout == NHWC {
		return []int{1, s.Height, s.Width, s.Channels()}
	}
	return []int{1, s.Channels(), s.Height, s.Width}
}

// Transform maps tensor coordinates back to frame coordinates:
// frameX = (tensorX - OffsetX) / ScaleX.
type Transform struct {
	ScaleX, ScaleY   float64
	OffsetX, OffsetY float64
	FrameW, FrameH   int
}

// ToFrame converts a point in tensor pixels to frame pixels.
func (t Transform) ToFrame(x, y float64) (float64, float64) {
	return (x - t.OffsetX) / t.ScaleX, (y - t.OffsetY) / t.ScaleY
}

// ToFrameNorm converts a point normalised to 0..1 of the tensor to frame pixels.
func (t Transform) ToFrameNorm(x, y float64, s Spec) (float64, float64) {
	return t.ToFrame(x*float64(s.Width), y*float64(s.Height))
}

var ErrShort = errors.New("tensor: destination too small")

var rgbaConv sync.Map // camera.PixelFormat -> *pixconv.Converter

// Fill writes the tensor for frame f into dst (at least s.Bytes() long) and
// returns how tensor coordinates map back onto the frame. The frame is not
// released.
func Fill(dst []byte, f *camera.Frame, s Spec) (Transform, error) {
	if s.Width <= 0 || s.Height <= 0 {
		return Transform{}, fmt.Errorf("tensor: bad size %dx%d", s.Width, s.Height)
	}
	if len(dst) < s.Bytes() {
		return Transform{}, ErrShort
	}
	src := f
	if f.Format != camera.RGBA && f.Format != camera.BGRA {
		v, ok := rgbaConv.Load(f.Format)
		if !ok {
			c, err := pixconv.NewConverter(f.Format, camera.RGBA)
			if err != nil {
				return Transform{}, err
			}
			v, _ = rgbaConv.LoadOrStore(f.Format, c)
		}
		var err error
		src, err = v.(*pixconv.Converter).Convert(f)
		if err != nil {
			return Transform{}, err
		}
		defer src.Release()
	}

	// Geometry: the frame region [sx0, sx0+sw) x [sy0, sy0+sh) lands on the
	// tensor region [dx0, dx0+dw) x [dy0, dy0+dh).
	fw, fh := float64(f.Width), float64(f.Height)
	tw, th := float64(s.Width), float64(s.Height)
	sx0, sy0, sw, sh := 0.0, 0.0, fw, fh
	dx0, dy0, dw, dh := 0, 0, s.Width, s.Height
	switch s.Resize {
	case Letterbox:
		k := math.Min(tw/fw, th/fh)
		dw, dh = int(math.Round(fw*k)), int(math.Round(fh*k))
		dx0, dy0 = (s.Width-dw)/2, (s.Height-dh)/2
	case CenterCrop:
		k := math.Max(tw/fw, th/fh)
		sw, sh = tw/k, th/k
		sx0, sy0 = (fw-sw)/2, (fh-sh)/2
	}
	tr := Transform{
		ScaleX: float64(dw) / sw, ScaleY: float64(dh) / sh,
		FrameW: f.Width, FrameH: f.Height,
	}
	tr.OffsetX = float64(dx0) - sx0*tr.ScaleX
	tr.OffsetY = float64(dy0) - sy0*tr.ScaleY

	w := writer{s: s, dst: dst}
	w.init()
	ri, bi := 0, 2
	if src.Format == camera.BGRA {
		ri, bi = 2, 0
	}
	plane, stride := src.Planes[0], src.Strides[0]
	px := func(x, y int) (float32, float32, float32) {
		p := plane[y*stride+4*x:]
		return float32(p[ri]), float32(p[1]), float32(p[bi])
	}
	kx, ky := sw/float64(dw), sh/float64(dh) // source pixels per tensor pixel
	parallelRows(s.Height, s.Width*s.Height, func(lo, hi int) {
		for ty := lo; ty < hi; ty++ {
			for tx := 0; tx < s.Width; tx++ {
				if tx < dx0 || tx >= dx0+dw || ty < dy0 || ty >= dy0+dh {
					w.put(tx, ty, float32(s.Pad[0]), float32(s.Pad[1]), float32(s.Pad[2]))
					continue
				}
				// Source footprint of this tensor pixel.
				fx0 := sx0 + float64(tx-dx0)*kx
				fy0 := sy0 + float64(ty-dy0)*ky
				var r, g, b float32
				if kx > 1.5 || ky > 1.5 {
					r, g, b = box(px, fx0, fy0, kx, ky, f.Width, f.Height)
				} else {
					r, g, b = bilinear(px, fx0+kx/2-0.5, fy0+ky/2-0.5, f.Width, f.Height)
				}
				w.put(tx, ty, r, g, b)
			}
		}
	})
	return tr, nil
}

type pixFn func(x, y int) (float32, float32, float32)

func box(px pixFn, x0, y0, kx, ky float64, W, H int) (float32, float32, float32) {
	ix0, iy0 := int(x0), int(y0)
	ix1, iy1 := int(math.Ceil(x0+kx)), int(math.Ceil(y0+ky))
	ix1, iy1 = min(max(ix1, ix0+1), W), min(max(iy1, iy0+1), H)
	// Cap the samples per axis so very large downscales stay cheap.
	sx := max(1, (ix1-ix0)/8)
	sy := max(1, (iy1-iy0)/8)
	var r, g, b float32
	n := 0
	for y := iy0; y < iy1; y += sy {
		for x := ix0; x < ix1; x += sx {
			pr, pg, pb := px(x, y)
			r, g, b = r+pr, g+pg, b+pb
			n++
		}
	}
	k := 1 / float32(n)
	return r * k, g * k, b * k
}

func bilinear(px pixFn, x, y float64, W, H int) (float32, float32, float32) {
	x = math.Max(0, math.Min(x, float64(W-1)))
	y = math.Max(0, math.Min(y, float64(H-1)))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, W-1), min(y0+1, H-1)
	ax, ay := float32(x-float64(x0)), float32(y-float64(y0))
	r00, g00, b00 := px(x0, y0)
	r10, g10, b10 := px(x1, y0)
	r01, g01, b01 := px(x0, y1)
	r11, g11, b11 := px(x1, y1)
	lerp := func(a, b, c, d float32) float32 {
		top := a + (b-a)*ax
		bot := c + (d-c)*ax
		return top + (bot-top)*ay
	}
	return lerp(r00, r10, r01, r11), lerp(g00, g10, g01, g11), lerp(b00, b10, b01, b11)
}

type writer struct {
	s     Spec
	dst   []byte
	scale [3]float32 // per output channel, applied to 0..255
	bias  [3]float32
	order binary.ByteOrder
}

func (w *writer) init() {
	s := w.s
	sc := s.Scale
	if sc == 0 {
		sc = 1
	}
	w.order = binary.NativeEndian
	if s.BigEndian {
		w.order = binary.BigEndian
	}
	for c := 0; c < 3; c++ {
		std := s.Std[c]
		if std == 0 {
			std = 1
		}
		w.scale[c] = sc / 255 / std
		w.bias[c] = -s.Mean[c] / std
	}
}

// put writes pixel (x, y) given R, G, B in 0..255.
func (w *writer) put(x, y int, r, g, b float32) {
	s := w.s
	if s.Order == Gray {
		v := 0.299*r + 0.587*g + 0.114*b
		w.elemC(y*s.Width+x, 0, v)
		return
	}
	vals := [3]float32{r, g, b}
	for c := 0; c < 3; c++ {
		src := c
		if s.Order == BGR {
			src = 2 - c
		}
		var i int
		if s.Layout == NHWC {
			i = (y*s.Width+x)*3 + c
		} else {
			i = c*s.Width*s.Height + y*s.Width + x
		}
		w.elemC(i, src, vals[src])
	}
}

// elemC writes element i from a 0..255 value of RGB channel c.
func (w *writer) elemC(i, c int, v float32) {
	switch w.s.DType {
	case Uint8:
		w.dst[i] = byte(min(max(v+0.5, 0), 255))
	case Int8:
		w.dst[i] = byte(int8(min(max(v+0.5, 0), 255) - 128))
	case Float32:
		w.order.PutUint32(w.dst[4*i:], math.Float32bits(v*w.scale[c]+w.bias[c]))
	case Float16:
		w.order.PutUint16(w.dst[2*i:], F16(v*w.scale[c]+w.bias[c]))
	}
}

// F16 converts a float32 to IEEE 754 half precision, rounding to nearest even.
func F16(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b>>23)&0xff) - 127 + 15
	mant := b & 0x7fffff
	switch {
	case (b>>23)&0xff == 0xff: // Inf or NaN
		if mant != 0 {
			return sign | 0x7e00
		}
		return sign | 0x7c00
	case exp >= 0x1f:
		return sign | 0x7c00
	case exp <= 0:
		if exp < -10 {
			return sign
		}
		mant |= 0x800000
		shift := uint(14 - exp)
		half := uint32(1) << (shift - 1)
		r := mant >> shift
		rem := mant & (1<<shift - 1)
		if rem > half || (rem == half && r&1 == 1) {
			r++
		}
		return sign | uint16(r)
	}
	r := uint32(exp)<<10 | mant>>13
	rem := mant & 0x1fff
	if rem > 0x1000 || (rem == 0x1000 && r&1 == 1) {
		r++ // may carry into the exponent, which is correct
	}
	return sign | uint16(r)
}

// F16ToF32 converts IEEE 754 half precision to float32.
func F16ToF32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	mant := uint32(h & 0x3ff)
	switch {
	case exp == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | mant<<13)
	case exp == 0:
		if mant == 0 {
			return math.Float32frombits(sign)
		}
		for mant&0x400 == 0 {
			mant <<= 1
			exp--
		}
		exp++
		mant &= 0x3ff
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | mant<<13)
}

func parallelRows(n, work int, fn func(lo, hi int)) {
	k := min(runtime.GOMAXPROCS(0), 8)
	if work < 1<<15 || k == 1 || n < 2*k {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	step := (n + k - 1) / k
	for lo := 0; lo < n; lo += step {
		hi := min(lo+step, n)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(lo, hi)
		}()
	}
	wg.Wait()
}

// FromShape returns a Spec for a model input of the given shape: [1, C, H,
// W] is NCHW and [1, H, W, C] is NHWC (C is 1 or 3; a leading batch of 1
// may be left out). Element type, normalisation and resize stay at their
// zero values for the caller to set.
func FromShape(dims []int) (Spec, error) {
	d := dims
	if len(d) == 4 {
		if d[0] != 1 {
			return Spec{}, fmt.Errorf("tensor: batch %d in shape %v, want 1", d[0], dims)
		}
		d = d[1:]
	}
	if len(d) != 3 {
		return Spec{}, fmt.Errorf("tensor: shape %v is not an image", dims)
	}
	ch := func(c int) bool { return c == 1 || c == 3 }
	var s Spec
	switch {
	case ch(d[0]) && !ch(d[2]):
		s = Spec{Layout: NCHW, Height: d[1], Width: d[2]}
		if d[0] == 1 {
			s.Order = Gray
		}
	case ch(d[2]):
		s = Spec{Layout: NHWC, Height: d[0], Width: d[1]}
		if d[2] == 1 {
			s.Order = Gray
		}
	default:
		return Spec{}, fmt.Errorf("tensor: shape %v has no 1- or 3-channel axis", dims)
	}
	return s, nil
}

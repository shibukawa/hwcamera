// Package pixconv converts camera frames between pixel formats in Go.
//
// YUV data from cameras is treated as BT.601 in video range (16-235), which
// is what UVC cameras, AVFoundation's '420v' and Media Foundation deliver.
// JPEG (MJPEG) is BT.601 full range per JFIF and is compressed to video
// range when it becomes YUV, so every NV12 frame has the same meaning
// whatever the camera sent.
package pixconv

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"runtime"
	"sync"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// Converter turns frames of one format into another, reusing buffers.
type Converter struct {
	from, to camera.PixelFormat
	pool     sync.Pool // *[]byte
}

// NewConverter returns a converter, or an error when the pair is not supported.
func NewConverter(from, to camera.PixelFormat) (*Converter, error) {
	if !to.Raw() {
		return nil, fmt.Errorf("pixconv: cannot produce %s", to)
	}
	switch from {
	case camera.NV12, camera.I420, camera.YUYV, camera.UYVY, camera.RGBA, camera.BGRA, camera.MJPEG:
	default:
		return nil, fmt.Errorf("pixconv: cannot read %s", from)
	}
	return &Converter{from: from, to: to}, nil
}

// Layout returns the plane sizes and strides of a tightly packed frame.
func Layout(f camera.PixelFormat, w, h int) (sizes, strides []int) {
	cw, ch := (w+1)/2, (h+1)/2
	switch f {
	case camera.NV12:
		return []int{w * h, 2 * cw * ch}, []int{w, 2 * cw}
	case camera.I420:
		return []int{w * h, cw * ch, cw * ch}, []int{w, cw, cw}
	case camera.YUYV, camera.UYVY:
		return []int{2 * cw * 2 * h}, []int{4 * cw}
	case camera.RGBA, camera.BGRA:
		return []int{4 * w * h}, []int{4 * w}
	}
	return nil, nil
}

// NewFrame allocates a Go-owned frame.
func NewFrame(f camera.PixelFormat, w, h int) *camera.Frame {
	sizes, strides := Layout(f, w, h)
	total := 0
	for _, s := range sizes {
		total += s
	}
	return frameIn(make([]byte, total), f, w, h, sizes, strides)
}

func frameIn(buf []byte, f camera.PixelFormat, w, h int, sizes, strides []int) *camera.Frame {
	fr := &camera.Frame{Width: w, Height: h, Format: f, Strides: strides}
	off := 0
	for _, s := range sizes {
		fr.Planes = append(fr.Planes, buf[off:off+s:off+s])
		off += s
	}
	return fr
}

// Convert returns a new frame in the target format. The source is not
// released. The result's Release returns its buffer to the converter's pool.
func (c *Converter) Convert(src *camera.Frame) (*camera.Frame, error) {
	if src.Format != c.from {
		return nil, fmt.Errorf("pixconv: frame is %s, converter reads %s", src.Format, c.from)
	}
	w, h := src.Width, src.Height
	sizes, strides := Layout(c.to, w, h)
	total := 0
	for _, s := range sizes {
		total += s
	}
	var bp *[]byte
	if v := c.pool.Get(); v != nil {
		bp = v.(*[]byte)
		if cap(*bp) < total {
			bp = nil
		}
	}
	if bp == nil {
		b := make([]byte, total)
		bp = &b
	}
	buf := (*bp)[:total]
	dst := frameIn(buf, c.to, w, h, sizes, strides)
	dst.Timestamp, dst.Sequence = src.Timestamp, src.Sequence
	if err := ConvertInto(dst, src); err != nil {
		c.pool.Put(bp)
		return nil, err
	}
	camera.SetRelease(dst, func() { c.pool.Put(bp) })
	return dst, nil
}

// ConvertInto converts src into dst, which must have the same size and
// planes large enough for its format.
func ConvertInto(dst, src *camera.Frame) error {
	if dst.Width != src.Width || dst.Height != src.Height {
		return fmt.Errorf("pixconv: size mismatch %dx%d -> %dx%d", src.Width, src.Height, dst.Width, dst.Height)
	}
	if src.Format == camera.MJPEG {
		img, err := jpeg.Decode(bytes.NewReader(FixMJPEG(src.Planes[0])))
		if err != nil {
			return fmt.Errorf("pixconv: MJPEG frame: %w", err)
		}
		b := img.Bounds()
		if b.Dx() != dst.Width || b.Dy() != dst.Height {
			return fmt.Errorf("pixconv: JPEG is %dx%d, frame says %dx%d", b.Dx(), b.Dy(), dst.Width, dst.Height)
		}
		return fromImage(dst, img)
	}
	if src.Format == dst.Format {
		copyFrame(dst, src)
		return nil
	}
	switch {
	case isRGB(src.Format) && isRGB(dst.Format):
		swapRB(dst, src)
		return nil
	case isRGB(dst.Format):
		yuvToRGB(dst, src)
		return nil
	case isRGB(src.Format):
		rgbToYUV(dst, src)
		return nil
	default:
		yuvToYUV(dst, src)
		return nil
	}
}

func isRGB(f camera.PixelFormat) bool { return f == camera.RGBA || f == camera.BGRA }

func copyFrame(dst, src *camera.Frame) {
	_, strides := Layout(dst.Format, dst.Width, dst.Height)
	rows := planeRows(dst.Format, dst.Height)
	for p := range dst.Planes {
		n := strides[p]
		for y := 0; y < rows[p]; y++ {
			copy(dst.Planes[p][y*dst.Strides[p]:y*dst.Strides[p]+n], src.Planes[p][y*src.Strides[p]:])
		}
	}
}

func planeRows(f camera.PixelFormat, h int) []int {
	ch := (h + 1) / 2
	switch f {
	case camera.NV12:
		return []int{h, ch}
	case camera.I420:
		return []int{h, ch, ch}
	}
	return []int{h}
}

// parallel runs fn over [0,n) split in bands across cores when the work is large.
func parallel(n int, work int, fn func(lo, hi int)) {
	k := runtime.GOMAXPROCS(0)
	if work < 1<<18 || k == 1 || n < 2*k {
		fn(0, n)
		return
	}
	if k > 8 {
		k = 8
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

// yuvReader exposes any YUV frame as Y at full resolution and Cb/Cr at 4:2:0
// (4:2:2 sources are averaged vertically).
type yuvReader struct{ f *camera.Frame }

func (r yuvReader) y(x, y int) byte {
	f := r.f
	switch f.Format {
	case camera.NV12, camera.I420:
		return f.Planes[0][y*f.Strides[0]+x]
	case camera.YUYV:
		return f.Planes[0][y*f.Strides[0]+2*x]
	case camera.UYVY:
		return f.Planes[0][y*f.Strides[0]+2*x+1]
	}
	return 0
}

// c420 returns Cb, Cr for chroma position (cx, cy) of the 4:2:0 grid.
func (r yuvReader) c420(cx, cy int) (byte, byte) {
	f := r.f
	switch f.Format {
	case camera.NV12:
		o := cy*f.Strides[1] + 2*cx
		return f.Planes[1][o], f.Planes[1][o+1]
	case camera.I420:
		return f.Planes[1][cy*f.Strides[1]+cx], f.Planes[2][cy*f.Strides[2]+cx]
	case camera.YUYV, camera.UYVY:
		y0 := 2 * cy
		y1 := min(y0+1, f.Height-1)
		cb, cr := 1, 3
		if f.Format == camera.UYVY {
			cb, cr = 0, 2
		}
		a := f.Planes[0][y0*f.Strides[0]+4*cx:]
		b := f.Planes[0][y1*f.Strides[0]+4*cx:]
		return byte((int(a[cb]) + int(b[cb]) + 1) >> 1), byte((int(a[cr]) + int(b[cr]) + 1) >> 1)
	}
	return 128, 128
}

// c422 returns Cb, Cr for chroma column cx on luma row y (exact for 4:2:2).
func (r yuvReader) c422(cx, y int) (byte, byte) {
	f := r.f
	switch f.Format {
	case camera.YUYV:
		a := f.Planes[0][y*f.Strides[0]+4*cx:]
		return a[1], a[3]
	case camera.UYVY:
		a := f.Planes[0][y*f.Strides[0]+4*cx:]
		return a[0], a[2]
	}
	return r.c420(cx, y/2)
}

func yuvToYUV(dst, src *camera.Frame) {
	w, h := dst.Width, dst.Height
	cw, ch := (w+1)/2, (h+1)/2
	r := yuvReader{src}
	switch dst.Format {
	case camera.NV12, camera.I420:
		parallel(h, w*h, func(lo, hi int) {
			for y := lo; y < hi; y++ {
				row := dst.Planes[0][y*dst.Strides[0]:]
				if src.Format == camera.NV12 || src.Format == camera.I420 {
					copy(row[:w], src.Planes[0][y*src.Strides[0]:])
					continue
				}
				for x := 0; x < w; x++ {
					row[x] = r.y(x, y)
				}
			}
		})
		parallel(ch, w*h, func(lo, hi int) {
			for cy := lo; cy < hi; cy++ {
				for cx := 0; cx < cw; cx++ {
					cb, cr := r.c420(cx, cy)
					if dst.Format == camera.NV12 {
						o := cy*dst.Strides[1] + 2*cx
						dst.Planes[1][o], dst.Planes[1][o+1] = cb, cr
					} else {
						dst.Planes[1][cy*dst.Strides[1]+cx] = cb
						dst.Planes[2][cy*dst.Strides[2]+cx] = cr
					}
				}
			}
		})
	case camera.YUYV, camera.UYVY:
		parallel(h, w*h, func(lo, hi int) {
			for y := lo; y < hi; y++ {
				row := dst.Planes[0][y*dst.Strides[0]:]
				for cx := 0; cx < cw; cx++ {
					x0 := 2 * cx
					x1 := min(x0+1, w-1)
					cb, cr := r.c422(cx, y)
					y0, y1 := r.y(x0, y), r.y(x1, y)
					if dst.Format == camera.YUYV {
						row[4*cx], row[4*cx+1], row[4*cx+2], row[4*cx+3] = y0, cb, y1, cr
					} else {
						row[4*cx], row[4*cx+1], row[4*cx+2], row[4*cx+3] = cb, y0, cr, y1
					}
				}
			}
		})
	}
}

func clamp(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// YCbCrToRGB converts one BT.601 video-range sample to RGB.
func YCbCrToRGB(y, cb, cr byte) (byte, byte, byte) {
	yy := (int32(y) - 16) * 76309 // 1.164 * 65536
	u := int32(cb) - 128
	v := int32(cr) - 128
	r := (yy + 104597*v + 32768) >> 16
	g := (yy - 25675*u - 53279*v + 32768) >> 16
	b := (yy + 132201*u + 32768) >> 16
	return clamp(r), clamp(g), clamp(b)
}

// RGBToYCbCr converts RGB to BT.601 video range.
func RGBToYCbCr(r, g, b byte) (byte, byte, byte) {
	R, G, B := int32(r), int32(g), int32(b)
	y := (16829*R + 33039*G + 6416*B + (16 << 16) + 32768) >> 16
	cb := (-9714*R - 19070*G + 28784*B + (128 << 16) + 32768) >> 16
	cr := (28784*R - 24103*G - 4681*B + (128 << 16) + 32768) >> 16
	return clamp(y), clamp(cb), clamp(cr)
}

func yuvToRGB(dst, src *camera.Frame) {
	w, h := dst.Width, dst.Height
	r := yuvReader{src}
	ri, bi := 0, 2
	if dst.Format == camera.BGRA {
		ri, bi = 2, 0
	}
	is422 := src.Format == camera.YUYV || src.Format == camera.UYVY
	parallel(h, w*h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			row := dst.Planes[0][y*dst.Strides[0]:]
			for x := 0; x < w; x++ {
				var cb, cr byte
				if is422 {
					cb, cr = r.c422(x/2, y)
				} else {
					cb, cr = r.c420(x/2, y/2)
				}
				R, G, B := YCbCrToRGB(r.y(x, y), cb, cr)
				p := row[4*x : 4*x+4]
				p[ri], p[1], p[bi], p[3] = R, G, B, 255
			}
		}
	})
}

func swapRB(dst, src *camera.Frame) {
	w, h := dst.Width, dst.Height
	parallel(h, w*h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			s := src.Planes[0][y*src.Strides[0]:]
			d := dst.Planes[0][y*dst.Strides[0]:]
			for x := 0; x < 4*w; x += 4 {
				d[x], d[x+1], d[x+2], d[x+3] = s[x+2], s[x+1], s[x], s[x+3]
			}
		}
	})
}

func rgbToYUV(dst, src *camera.Frame) {
	w, h := dst.Width, dst.Height
	ri, bi := 0, 2
	if src.Format == camera.BGRA {
		ri, bi = 2, 0
	}
	px := func(x, y int) (byte, byte, byte) {
		p := src.Planes[0][y*src.Strides[0]+4*x:]
		return p[ri], p[1], p[bi]
	}
	// Go through an I420/NV12 path; for 4:2:2 targets build NV12 first.
	if dst.Format == camera.YUYV || dst.Format == camera.UYVY {
		tmp := NewFrame(camera.NV12, w, h)
		rgbToYUV(tmp, src)
		yuvToYUV(dst, tmp)
		return
	}
	cw, ch := (w+1)/2, (h+1)/2
	parallel(ch, w*h, func(lo, hi int) {
		for cy := lo; cy < hi; cy++ {
			for cx := 0; cx < cw; cx++ {
				var sb, sr, n int
				for dy := 0; dy < 2; dy++ {
					y := 2*cy + dy
					if y >= h {
						continue
					}
					for dx := 0; dx < 2; dx++ {
						x := 2*cx + dx
						if x >= w {
							continue
						}
						R, G, B := px(x, y)
						Y, Cb, Cr := RGBToYCbCr(R, G, B)
						dst.Planes[0][y*dst.Strides[0]+x] = Y
						sb += int(Cb)
						sr += int(Cr)
						n++
					}
				}
				cb, cr := byte((sb+n/2)/n), byte((sr+n/2)/n)
				if dst.Format == camera.NV12 {
					o := cy*dst.Strides[1] + 2*cx
					dst.Planes[1][o], dst.Planes[1][o+1] = cb, cr
				} else {
					dst.Planes[1][cy*dst.Strides[1]+cx] = cb
					dst.Planes[2][cy*dst.Strides[2]+cx] = cr
				}
			}
		}
	})
}

// full-range JPEG to video range.
func fullToVideoY(v byte) byte { return byte((int(v)*219+127)/255 + 16) }
func fullToVideoC(v byte) byte {
	d := (int(v) - 128) * 224
	if d >= 0 {
		return byte((d+127)/255 + 128)
	}
	return byte(128 - (-d+127)/255)
}

func fromImage(dst *camera.Frame, img image.Image) error {
	w, h := dst.Width, dst.Height
	b := img.Bounds()
	if ycc, ok := img.(*image.YCbCr); ok && !isRGB(dst.Format) {
		// JPEG chroma subsampling varies (4:2:0, 4:2:2, 4:4:4); sample the
		// chroma at the centre of each 2x2 block through COffset.
		tmp := NewFrame(camera.I420, w, h)
		parallel(h, w*h, func(lo, hi int) {
			for y := lo; y < hi; y++ {
				row := tmp.Planes[0][y*tmp.Strides[0]:]
				src := ycc.Y[ycc.YOffset(b.Min.X, b.Min.Y+y):]
				for x := 0; x < w; x++ {
					row[x] = fullToVideoY(src[x])
				}
			}
		})
		cw, ch := (w+1)/2, (h+1)/2
		parallel(ch, w*h, func(lo, hi int) {
			for cy := lo; cy < hi; cy++ {
				for cx := 0; cx < cw; cx++ {
					x, y := b.Min.X+2*cx, b.Min.Y+2*cy
					y1 := min(y+1, b.Max.Y-1)
					o0, o1 := ycc.COffset(x, y), ycc.COffset(x, y1)
					cb := byte((int(ycc.Cb[o0]) + int(ycc.Cb[o1]) + 1) >> 1)
					cr := byte((int(ycc.Cr[o0]) + int(ycc.Cr[o1]) + 1) >> 1)
					tmp.Planes[1][cy*tmp.Strides[1]+cx] = fullToVideoC(cb)
					tmp.Planes[2][cy*tmp.Strides[2]+cx] = fullToVideoC(cr)
				}
			}
		})
		if dst.Format == camera.I420 {
			copyFrame(dst, tmp)
		} else {
			yuvToYUV(dst, tmp)
		}
		return nil
	}
	// Through RGBA.
	rgba := NewFrame(camera.RGBA, w, h)
	ri, bi := 0, 2
	if dst.Format == camera.BGRA {
		ri, bi = 2, 0
		rgba.Format = camera.BGRA
	}
	ycc, _ := img.(*image.YCbCr)
	gray, _ := img.(*image.Gray)
	parallel(h, w*h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			row := rgba.Planes[0][y*rgba.Strides[0]:]
			for x := 0; x < w; x++ {
				var r, g, bb byte
				switch {
				case ycc != nil:
					X, Y := b.Min.X+x, b.Min.Y+y
					r, g, bb = color.YCbCrToRGB(ycc.Y[ycc.YOffset(X, Y)], ycc.Cb[ycc.COffset(X, Y)], ycc.Cr[ycc.COffset(X, Y)])
				case gray != nil:
					v := gray.Pix[gray.PixOffset(b.Min.X+x, b.Min.Y+y)]
					r, g, bb = v, v, v
				default:
					R, G, B, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
					r, g, bb = byte(R>>8), byte(G>>8), byte(B>>8)
				}
				p := row[4*x : 4*x+4]
				p[ri], p[1], p[bi], p[3] = r, g, bb, 255
			}
		}
	})
	if isRGB(dst.Format) {
		copyFrame(dst, rgba)
		return nil
	}
	rgbToYUV(dst, rgba)
	return nil
}

// DecodeJPEG decodes an MJPEG frame into an image.
func DecodeJPEG(data []byte) (image.Image, error) {
	return jpeg.Decode(bytes.NewReader(FixMJPEG(data)))
}

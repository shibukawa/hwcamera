package camera2

// plane is one plane of an AImage in YUV_420_888: its bytes (exactly the
// length AImage_getPlaneData reports), row stride and pixel stride.
type plane struct {
	Data        []byte
	RowStride   int
	PixelStride int
}

// toNV12 packs the w x h region at (x0, y0) of a YUV_420_888 image into
// dst as NV12: the Y plane (stride w) followed by the interleaved CbCr
// plane (stride w). x0, y0, w and h must be even. dst must hold
// w*h + w*h/2 bytes.
//
// YUV_420_888 promises only that chroma is subsampled 2x2; the planes may
// be separate (pixel stride 1, I420-like), or U and V may interleave in
// one buffer (pixel stride 2) with U first (NV12 memory, uvInterleaved
// true: V's data begins one byte after U's) or V first (NV21 memory).
// The NV12 case copies rows; the others go pixel by pixel. Reads stay
// within each plane's reported length, which for interleaved chroma ends
// one byte short of the pair.
func toNV12(dst []byte, x0, y0, w, h int, y, u, v plane, uvInterleaved bool) {
	for r := 0; r < h; r++ {
		off := (y0+r)*y.RowStride + x0*y.PixelStride
		row := dst[r*w : r*w+w]
		if y.PixelStride == 1 {
			copy(row, y.Data[off:off+w])
			continue
		}
		for x := range row {
			row[x] = y.Data[off+x*y.PixelStride]
		}
	}
	uv := dst[w*h:]
	cw, ch := w/2, h/2
	cx, cy := x0/2, y0/2
	for r := 0; r < ch; r++ {
		row := uv[r*w : r*w+2*cw]
		uo := (cy+r)*u.RowStride + cx*u.PixelStride
		vo := (cy+r)*v.RowStride + cx*v.PixelStride
		if uvInterleaved && u.PixelStride == 2 && v.PixelStride == 2 && cw > 0 {
			// U V U V ... in memory: the row is NV12 already, except
			// that U's plane ends before the last V byte.
			copy(row[:2*cw-1], u.Data[uo:uo+2*cw-1])
			row[2*cw-1] = v.Data[vo+2*(cw-1)]
			continue
		}
		for x := 0; x < cw; x++ {
			row[2*x] = u.Data[uo+x*u.PixelStride]
			row[2*x+1] = v.Data[vo+x*v.PixelStride]
		}
	}
}

// nv12Size returns the bytes of a w x h NV12 frame with stride w.
func nv12Size(w, h int) int { return w*h + w*h/2 }

// covers reports whether the plane's data holds every sample of the w x h
// region at (x0, y0) (in the plane's own coordinates), so that a vendor
// reporting short planes cannot make the copy run past them.
func (p plane) covers(x0, y0, w, h int) bool {
	if w <= 0 || h <= 0 || x0 < 0 || y0 < 0 || p.RowStride <= 0 || p.PixelStride <= 0 {
		return false
	}
	last := (y0+h-1)*p.RowStride + (x0+w-1)*p.PixelStride
	return last < len(p.Data)
}

// cropWindow returns the even-aligned region of a w x h image to deliver
// given its crop rectangle (left, top, right, bottom; right and bottom
// exclusive). An absent, empty or out-of-bounds crop selects the whole
// image.
func cropWindow(w, h int, left, top, right, bottom int, ok bool) (x0, y0, cw, ch int) {
	x0, y0, cw, ch = 0, 0, w&^1, h&^1
	if !ok || left < 0 || top < 0 || right > w || bottom > h || right <= left || bottom <= top {
		return
	}
	x0, y0 = (left+1)&^1, (top+1)&^1
	cw, ch = (right-x0)&^1, (bottom-y0)&^1
	if cw <= 0 || ch <= 0 {
		return 0, 0, w &^ 1, h &^ 1
	}
	return
}

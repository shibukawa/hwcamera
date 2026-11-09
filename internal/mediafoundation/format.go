// Package mediafoundation implements the Windows camera backend on top of
// the Media Foundation source reader, called through raw COM vtables (no
// cgo). The platform-specific parts are in the windows-tagged files; this
// file and the other untagged ones are plain Go so that they can be unit
// tested on any OS.
package mediafoundation

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/mediafoundation/sys"
)

// Name is the backend identifier.
const Name = "mediafoundation"

// Backend is the Media Foundation camera backend.
type Backend struct{}

// Name implements camera.Backend.
func (Backend) Name() string { return Name }

// formatFromSubtype maps a Media Foundation video subtype to the pixel
// format of the samples, or camera.Unknown when the subtype is not handled.
func formatFromSubtype(g sys.GUID) camera.PixelFormat {
	switch g {
	case sys.MFVideoFormat_NV12:
		return camera.NV12
	case sys.MFVideoFormat_YUY2:
		return camera.YUYV
	case sys.MFVideoFormat_UYVY:
		return camera.UYVY
	case sys.MFVideoFormat_I420, sys.MFVideoFormat_IYUV:
		return camera.I420
	case sys.MFVideoFormat_MJPG:
		return camera.MJPEG
	case sys.MFVideoFormat_RGB32, sys.MFVideoFormat_ARGB32:
		// D3DFMT_X8R8G8B8 / A8R8G8B8: a little-endian 32-bit ARGB word,
		// so B, G, R, A in memory.
		return camera.BGRA
	}
	return camera.Unknown
}

// subtypeFor returns the subtype the source reader is asked for to deliver
// f, and whether there is one.
func subtypeFor(f camera.PixelFormat) (sys.GUID, bool) {
	switch f {
	case camera.NV12:
		return sys.MFVideoFormat_NV12, true
	case camera.YUYV:
		return sys.MFVideoFormat_YUY2, true
	case camera.UYVY:
		return sys.MFVideoFormat_UYVY, true
	case camera.I420:
		return sys.MFVideoFormat_IYUV, true
	case camera.BGRA:
		return sys.MFVideoFormat_RGB32, true
	case camera.MJPEG:
		return sys.MFVideoFormat_MJPG, true
	}
	return sys.GUID{}, false
}

// unpack splits a packed UINT64 attribute (MF_MT_FRAME_SIZE: width, height;
// MF_MT_FRAME_RATE: numerator, denominator) into its high and low halves
// (Unpack2UINT32AsUINT64 in mfapi.h).
func unpack(v uint64) (hi, lo uint32) { return uint32(v >> 32), uint32(v) }

// pack is the inverse of unpack.
func pack(hi, lo uint32) uint64 { return uint64(hi)<<32 | uint64(lo) }

// rateOf converts a frame-rate ratio to frames per second, 0 when unknown.
func rateOf(num, den uint32) float64 {
	if num == 0 || den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func sameRate(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// nativeType is one entry of IMFSourceReader::GetNativeMediaType.
type nativeType struct {
	index  uint32
	format camera.PixelFormat
	width  int
	height int
	rate   float64 // 0 when the type has no MF_MT_FRAME_RATE
}

// mergeModes groups native types by (format, size), merging their frame
// rates, and sorts the result. Types of unknown format are skipped.
func mergeModes(types []nativeType) []camera.Mode {
	type key struct {
		f    camera.PixelFormat
		w, h int
	}
	idx := map[key]int{}
	var modes []camera.Mode
	for _, t := range types {
		if t.format == camera.Unknown || t.width <= 0 || t.height <= 0 {
			continue
		}
		k := key{t.format, t.width, t.height}
		i, ok := idx[k]
		if !ok {
			i = len(modes)
			idx[k] = i
			modes = append(modes, camera.Mode{Format: t.format, Width: t.width, Height: t.height})
		}
		if t.rate <= 0 {
			continue
		}
		dup := false
		for _, r := range modes[i].FrameRates {
			if sameRate(r, t.rate) {
				dup = true
				break
			}
		}
		if !dup {
			modes[i].FrameRates = append(modes[i].FrameRates, t.rate)
		}
	}
	camera.SortModes(modes)
	return modes
}

// pickNative returns the native type matching the chosen mode and frame
// rate (rate 0 matches any), or false.
func pickNative(types []nativeType, m camera.Mode, rate float64) (nativeType, bool) {
	var fallback *nativeType
	for i, t := range types {
		if t.format != m.Format || t.width != m.Width || t.height != m.Height {
			continue
		}
		if rate == 0 || sameRate(t.rate, rate) {
			return t, true
		}
		if fallback == nil || t.rate > fallback.rate {
			fallback = &types[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return nativeType{}, false
}

// outputFormat decides which format to ask the source reader to deliver
// for a device running natively in native, given the request. It returns
// camera.Unknown to keep the native format. Media Foundation decodes MJPEG
// (and converts between YUV layouts) faster than the pure-Go fallbacks, so
// it does that work when the caller wants a raw format.
func outputFormat(native camera.PixelFormat, cfg camera.Config) camera.PixelFormat {
	want := cfg.Format
	switch {
	case native == want:
		return camera.Unknown
	case native == camera.MJPEG:
		if want == camera.Unknown {
			if cfg.Native == camera.MJPEG {
				return camera.Unknown // explicitly asked for the JPEG bytes
			}
			return camera.NV12
		}
		if _, ok := subtypeFor(want); ok && want.Raw() {
			return want
		}
		return camera.NV12
	case want == camera.NV12:
		return camera.NV12
	}
	return camera.Unknown
}

// rect is the visible picture area inside a decoded image.
type rect struct{ x, y, w, h int }

// decodeVideoArea parses an MFVideoArea blob (two MFOffset values followed
// by a SIZE) as stored in MF_MT_MINIMUM_DISPLAY_APERTURE. The fractional
// parts of the offsets are ignored.
func decodeVideoArea(b []byte) (rect, bool) {
	if len(b) < 16 {
		return rect{}, false
	}
	r := rect{
		x: int(int16(binary.LittleEndian.Uint16(b[2:]))),
		y: int(int16(binary.LittleEndian.Uint16(b[6:]))),
		w: int(int32(binary.LittleEndian.Uint32(b[8:]))),
		h: int(int32(binary.LittleEndian.Uint32(b[12:]))),
	}
	if r.x < 0 || r.y < 0 || r.w <= 0 || r.h <= 0 {
		return rect{}, false
	}
	return r, true
}

// layout describes how the samples of the current media type are laid out.
type layout struct {
	format camera.PixelFormat
	// width, height is the coded size (MF_MT_FRAME_SIZE); height is also
	// the number of rows allocated per plane before the next one starts.
	width, height int
	visible       rect
	// stride is MF_MT_DEFAULT_STRIDE (negative for bottom-up), 0 when
	// absent. Used only when the buffer has no 2D interface.
	stride int
}

// plane describes where one plane of an image sits relative to scanline 0.
type plane struct {
	start    int // byte offset of the plane's row 0 (before cropping)
	pitch    int // byte distance between rows, negative for bottom-up
	x, y     int // crop origin in bytes and rows
	rows     int // rows to copy
	rowBytes int // bytes per row to copy
}

// planesFor returns the planes of a raw image with the given plane-0 pitch.
// allocRows is the number of rows plane 0 occupies before the next plane;
// v is the visible area to copy.
func planesFor(f camera.PixelFormat, pitch, allocRows int, v rect) ([]plane, error) {
	if pitch == 0 {
		return nil, errors.New("zero pitch")
	}
	if v.w <= 0 || v.h <= 0 || v.x < 0 || v.y < 0 || v.y+v.h > allocRows {
		return nil, fmt.Errorf("visible area %+v outside %d rows", v, allocRows)
	}
	abs := pitch
	if abs < 0 {
		abs = -abs
	}
	var ps []plane
	switch f {
	case camera.NV12:
		cx, cy := v.x&^1, v.y/2
		ps = []plane{
			{start: 0, pitch: pitch, x: v.x, y: v.y, rows: v.h, rowBytes: v.w},
			{start: pitch * allocRows, pitch: pitch, x: cx, y: cy, rows: (v.h + 1) / 2, rowBytes: (v.w + 1) / 2 * 2},
		}
	case camera.I420:
		cp := pitch / 2
		cx, cy := v.x/2, v.y/2
		cw, ch := (v.w+1)/2, (v.h+1)/2
		u := pitch * allocRows
		ps = []plane{
			{start: 0, pitch: pitch, x: v.x, y: v.y, rows: v.h, rowBytes: v.w},
			{start: u, pitch: cp, x: cx, y: cy, rows: ch, rowBytes: cw},
			{start: u + cp*((allocRows+1)/2), pitch: cp, x: cx, y: cy, rows: ch, rowBytes: cw},
		}
	case camera.YUYV, camera.UYVY:
		ps = []plane{{start: 0, pitch: pitch, x: (v.x &^ 1) * 2, y: v.y, rows: v.h, rowBytes: (v.w + 1) / 2 * 4}}
	case camera.BGRA, camera.RGBA:
		ps = []plane{{start: 0, pitch: pitch, x: v.x * 4, y: v.y, rows: v.h, rowBytes: v.w * 4}}
	default:
		return nil, fmt.Errorf("no 2D layout for %s", f)
	}
	for _, p := range ps {
		ap := p.pitch
		if ap < 0 {
			ap = -ap
		}
		if p.x+p.rowBytes > ap {
			return nil, fmt.Errorf("pitch %d too small for %d-byte rows of %s", abs, p.x+p.rowBytes, f)
		}
	}
	return ps, nil
}

// extent returns the byte range [lo, hi) relative to scanline 0 that the
// planes touch.
func extent(ps []plane) (lo, hi int) {
	first := true
	for _, p := range ps {
		for _, r := range []int{p.y, p.y + p.rows - 1} {
			a := p.start + r*p.pitch + p.x
			b := a + p.rowBytes
			if first || a < lo {
				lo = a
			}
			if first || b > hi {
				hi = b
			}
			first = false
		}
	}
	return lo, hi
}

// copyPlanes copies the planes out of src, where scanline 0 is at src[base],
// into one tightly packed Go buffer. It fails rather than read outside src.
func copyPlanes(src []byte, base int, ps []plane) ([][]byte, []int, error) {
	total := 0
	for _, p := range ps {
		total += p.rows * p.rowBytes
	}
	buf := make([]byte, total)
	planes := make([][]byte, len(ps))
	strides := make([]int, len(ps))
	off := 0
	for i, p := range ps {
		dst := buf[off : off+p.rows*p.rowBytes : off+p.rows*p.rowBytes]
		for r := 0; r < p.rows; r++ {
			s := base + p.start + (p.y+r)*p.pitch + p.x
			if s < 0 || s+p.rowBytes > len(src) {
				return nil, nil, fmt.Errorf("plane %d row %d outside the %d-byte buffer", i, r, len(src))
			}
			copy(dst[r*p.rowBytes:(r+1)*p.rowBytes], src[s:s+p.rowBytes])
		}
		planes[i] = dst
		strides[i] = p.rowBytes
		off += p.rows * p.rowBytes
	}
	return planes, strides, nil
}

// inferAllocRows guesses the rows allocated for plane 0 of a 4:2:0 image
// from the total length of a 2D buffer, for decoders that pad the surface
// height (1080 coded as 1088) without saying so in MF_MT_FRAME_SIZE. It
// only accepts an exact match within 64 rows of the stated height and
// otherwise returns height.
func inferAllocRows(f camera.PixelFormat, pitch, height, bufLen int) int {
	if pitch <= 0 || (f != camera.NV12 && f != camera.I420) {
		return height
	}
	for rows := height; rows <= height+64; rows++ {
		var size int
		if f == camera.NV12 {
			size = pitch*rows + pitch*((rows+1)/2)
		} else {
			size = pitch*rows + 2*(pitch/2)*((rows+1)/2)
		}
		if size == bufLen {
			return rows
		}
		if size > bufLen {
			break
		}
	}
	return height
}

// tightStride is the row size of plane 0 without padding.
func tightStride(f camera.PixelFormat, w int) int {
	switch f {
	case camera.YUYV, camera.UYVY:
		return (w + 1) / 2 * 4
	case camera.BGRA, camera.RGBA:
		return w * 4
	}
	return w
}

// clock turns sample times into frame timestamps and sequence numbers.
type clock struct {
	started  bool
	t0, last int64 // 100 ns
	seq      uint64
	frameDur int64 // nominal frame duration in 100 ns, 0 when unknown
	ticks    int   // stream ticks (gaps) since the last sample
}

// tick records an MF_SOURCE_READERF_STREAMTICK: the device skipped data.
func (c *clock) tick() { c.ticks++ }

// next returns the timestamp and sequence number of a sample with sample
// time ts (100 ns). The first sample is time 0, sequence 0; afterwards the
// sequence advances by one per sample plus the frames missing from a gap
// in the sample times or reported by stream ticks.
func (c *clock) next(ts int64) (time.Duration, uint64) {
	if !c.started {
		c.started = true
		c.t0, c.last = ts, ts
		c.ticks = 0
		return 0, 0
	}
	missed := 0
	if d := ts - c.last; c.frameDur > 0 && d > c.frameDur*3/2 {
		missed = int((d+c.frameDur/2)/c.frameDur) - 1
	}
	missed = max(missed, c.ticks)
	c.ticks = 0
	c.seq += 1 + uint64(missed)
	c.last = ts
	rel := ts - c.t0
	if rel < 0 {
		rel = 0
	}
	return time.Duration(rel) * 100, c.seq
}

// hrError wraps a failed HRESULT, mapping the codes callers can act on to
// the camera sentinel errors.
func hrError(op string, hr sys.HRESULT) error {
	e := &camera.BackendError{Backend: Name, Op: op, Code: int64(uint32(hr))}
	switch hr {
	case sys.E_ACCESSDENIED:
		e.Err = fmt.Errorf("%w (Settings > Privacy > Camera)", camera.ErrPermission)
	case sys.MF_E_HW_MFT_FAILED_START_STREAMING,
		sys.MF_E_VIDEO_RECORDING_DEVICE_INVALIDATED,
		sys.MF_E_VIDEO_RECORDING_DEVICE_PREEMPTED,
		sys.MF_E_VIDEO_DEVICE_LOCKED,
		sys.HRESULT_SHARING_VIOLATION,
		sys.HRESULT_BUSY,
		sys.HRESULT_DEVICE_IN_USE:
		e.Err = fmt.Errorf("%w: %s", camera.ErrBusy, hr.Name())
	case sys.MF_E_SHUTDOWN:
		e.Err = camera.ErrClosed
	default:
		if n := hr.Name(); n != "" {
			e.Err = errors.New(n)
		}
	}
	return e
}

// Panel values of the ACPI _PLD buffer (Physical Location of Device).
const (
	panelTop     = 0
	panelBottom  = 1
	panelLeft    = 2
	panelRight   = 3
	panelFront   = 4
	panelBack    = 5
	panelUnknown = 6
)

// pldPanel extracts the panel field (bits 67..69: byte 8, bits 3..5) of an
// ACPI _PLD buffer, or -1.
func pldPanel(b []byte) int {
	if len(b) < 16 {
		return -1
	}
	return int(b[8]>>3) & 7
}

// isVirtualLink reports a software camera from its symbolic link: devices
// enumerated by the root bus (DirectShow/MF filters registered as devices)
// or created through the virtual camera API of Windows 11 (SWD#VCAMDEVAPI,
// OBS 30 and later) have no hardware bus in the path.
func isVirtualLink(symlink string) bool {
	l := strings.ToLower(symlink)
	return strings.HasPrefix(l, `\\?\root#`) || strings.HasPrefix(l, `\\?\swd#`)
}

// positionOf guesses where a camera sits from its symbolic link, the panel
// of its _PLD (-1 when absent) and whether Windows considers it part of the
// computer itself (inLocal; nil when unknown).
func positionOf(symlink string, panel int, inLocal *bool) camera.Position {
	if inLocal != nil && !*inLocal {
		return camera.PositionExternal // its own device container: plugged in
	}
	switch panel {
	case panelFront:
		return camera.PositionFront
	case panelBack:
		return camera.PositionBack
	}
	if strings.Contains(strings.ToLower(symlink), "usb#") {
		if inLocal != nil && *inLocal {
			return camera.PositionFront // built-in USB webcam (laptop lid)
		}
		return camera.PositionExternal
	}
	return camera.PositionUnknown
}

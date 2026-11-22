// Package camera holds the types shared by the public hwcamera package and
// the operating-system backends, so the backends can live in internal
// packages without importing the public one.
package camera

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PixelFormat names the memory layout of a frame.
type PixelFormat int

const (
	// Unknown is the zero value: "any" in a request, never in a frame.
	Unknown PixelFormat = iota
	// NV12 is 8-bit 4:2:0: a full-resolution Y plane and one half-resolution
	// plane of interleaved Cb, Cr. Two planes. The input format of every
	// hardware video encoder.
	NV12
	// I420 is 8-bit 4:2:0 with separate Cb and Cr planes. Three planes.
	I420
	// YUYV is 8-bit 4:2:2 packed as Y0 Cb Y1 Cr (V4L2 YUYV, Windows YUY2,
	// Core Video 'yuvs'). One plane.
	YUYV
	// UYVY is 8-bit 4:2:2 packed as Cb Y0 Cr Y1 (Core Video '2vuy'). One plane.
	UYVY
	// RGBA is packed 8-bit R, G, B, A. One plane.
	RGBA
	// BGRA is packed 8-bit B, G, R, A. One plane.
	BGRA
	// MJPEG is one JPEG image per frame (most USB cameras at high
	// resolutions). One plane holding the compressed bytes; Strides[0] is 0.
	MJPEG
)

func (f PixelFormat) String() string {
	switch f {
	case NV12:
		return "NV12"
	case I420:
		return "I420"
	case YUYV:
		return "YUYV"
	case UYVY:
		return "UYVY"
	case RGBA:
		return "RGBA"
	case BGRA:
		return "BGRA"
	case MJPEG:
		return "MJPEG"
	case Unknown:
		return "any"
	}
	return fmt.Sprintf("PixelFormat(%d)", int(f))
}

// Raw reports whether the format holds uncompressed pixels.
func (f PixelFormat) Raw() bool { return f != MJPEG && f != Unknown }

// Position tells where a camera sits.
type Position int

const (
	PositionUnknown  Position = iota
	PositionFront             // facing the user (laptop lid, phone selfie)
	PositionBack              // facing away from the user
	PositionExternal          // USB, network or virtual camera
)

func (p Position) String() string {
	switch p {
	case PositionFront:
		return "front"
	case PositionBack:
		return "back"
	case PositionExternal:
		return "external"
	}
	return "unknown"
}

// Lens tells which lens of a multi-camera phone a camera is.
type Lens int

const (
	LensUnknown   Lens = iota
	LensWide           // the main lens (about 24-28 mm equivalent)
	LensUltraWide      // the 0.5x lens
	LensTelephoto      // a 2x or longer lens
)

func (l Lens) String() string {
	switch l {
	case LensWide:
		return "wide"
	case LensUltraWide:
		return "ultrawide"
	case LensTelephoto:
		return "telephoto"
	}
	return "unknown"
}

// Kind is a set of camera traits, combined with |. A camera matches a
// Kind when it has every trait in it: Back|UltraWide is the back
// ultra-wide camera, and Front|Back matches nothing.
//
// The first traits describe the device; the rest (resolution class and
// aspect ratio) describe a mode, and a device has them when one of its
// modes has.
type Kind uint32

const (
	KindFront Kind = 1 << iota
	KindBack
	KindExternal
	KindWide
	KindUltraWide
	KindTelephoto
	KindPhysical
	KindVirtual

	KindLow       // up to 480,000 pixels (800x600, 854x480, 640x480 and smaller)
	KindMid       // up to 1,228,800 pixels (1280x960, 1280x720, 1024x768)
	KindHigh      // more (1920x1080 and up)
	KindSquare    // width = height (within 2%)
	KindLandscape // wider than high (the default when no shape is given)
	KindPortrait  // higher than wide
	KindR4_3      // 4:3 or 3:4 (within 2%)
	KindR16_9     // 16:9 or 9:16 (within 2%)

	// DeviceKinds and ModeKinds split a Kind into its two halves.
	DeviceKinds = KindFront | KindBack | KindExternal | KindWide | KindUltraWide | KindTelephoto | KindPhysical | KindVirtual
	ModeKinds   = KindLow | KindMid | KindHigh | KindSquare | KindLandscape | KindPortrait | KindR4_3 | KindR16_9
	// SizeKinds are the resolution classes, ShapeKinds the rest.
	SizeKinds  = KindLow | KindMid | KindHigh
	ShapeKinds = KindSquare | KindLandscape | KindPortrait | KindR4_3 | KindR16_9
)

// Pixel counts that bound the resolution classes.
const (
	lowMaxPixels = 480_000
	midMaxPixels = 1_228_800
)

var kindNames = []struct {
	k    Kind
	name string
}{
	{KindFront, "front"}, {KindBack, "back"}, {KindExternal, "external"},
	{KindWide, "wide"}, {KindUltraWide, "ultrawide"}, {KindTelephoto, "telephoto"},
	{KindPhysical, "physical"}, {KindVirtual, "virtual"},
	{KindLow, "low"}, {KindMid, "mid"}, {KindHigh, "high"},
	{KindSquare, "square"}, {KindLandscape, "landscape"}, {KindPortrait, "portrait"},
	{KindR4_3, "4:3"}, {KindR16_9, "16:9"},
}

func (k Kind) String() string {
	if k == 0 {
		return "any"
	}
	var parts []string
	for _, n := range kindNames {
		if k&n.k != 0 {
			parts = append(parts, n.name)
			k &^= n.k
		}
	}
	if k != 0 {
		parts = append(parts, fmt.Sprintf("Kind(%#x)", uint32(k)))
	}
	return strings.Join(parts, "|")
}

// ParseKind parses traits such as "back", "back+ultrawide",
// "front|low" or "high,16:9" (separated by +, |, comma or space; case does
// not matter; "rear", "ultra-wide", "tele", "medium", "4x3" and "r16_9"
// are accepted too).
func ParseKind(s string) (Kind, error) {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == '+' || r == '|' || r == ',' || r == ' '
	})
	if len(fields) == 0 {
		return 0, fmt.Errorf("hwcamera: empty camera kind")
	}
	var k Kind
	for _, f := range fields {
		f = strings.NewReplacer("-", "", "_", "").Replace(f)
		switch f {
		case "rear":
			f = "back"
		case "tele":
			f = "telephoto"
		case "medium", "middle":
			f = "mid"
		case "4x3", "r43":
			f = "4:3"
		case "16x9", "r169":
			f = "16:9"
		}
		found := false
		for _, n := range kindNames {
			if n.name == f {
				k |= n.k
				found = true
			}
		}
		if !found {
			return 0, fmt.Errorf("hwcamera: unknown camera kind %q", f)
		}
	}
	return k, nil
}

// Mode is one capture mode a device offers: its native pixel format, size
// and the frame rates it can run at.
type Mode struct {
	Format     PixelFormat
	Width      int
	Height     int
	FrameRates []float64 // frames per second, highest first; empty when the device does not say
}

func (m Mode) String() string {
	s := fmt.Sprintf("%s %dx%d", m.Format, m.Width, m.Height)
	for i, r := range m.FrameRates {
		if i == 0 {
			s += " @"
		} else {
			s += ","
		}
		s += trimFloat(r)
	}
	return s
}

func trimFloat(f float64) string {
	if f == float64(int(f)) {
		return fmt.Sprintf("%d", int(f))
	}
	return fmt.Sprintf("%.2f", f)
}

// MaxFrameRate returns the highest frame rate of the mode, or 0.
func (m Mode) MaxFrameRate() float64 {
	best := 0.0
	for _, r := range m.FrameRates {
		if r > best {
			best = r
		}
	}
	return best
}

// DeviceInfo describes one camera.
type DeviceInfo struct {
	// ID is the backend's stable identifier: the device node on Linux
	// (/dev/video0), the AVCaptureDevice uniqueID on macOS, the symbolic
	// link on Windows. Pass it to Open.
	ID       string
	Name     string
	Backend  string
	Position Position
	// Lens is the kind of lens (iOS, Android and Mac built-in cameras;
	// unknown elsewhere).
	Lens Lens
	// Default reports the camera the operating system prefers (macOS:
	// the one chosen in Control Center). Not every backend knows.
	Default bool
	// Virtual reports a software camera (OBS, a VR or video-call
	// driver, v4l2loopback) rather than a physical one. Open with an empty
	// ID never picks one while a physical camera exists.
	Virtual bool
	Modes   []Mode
}

// Kind returns the device traits of the camera (position, lens,
// physical or virtual); the mode traits differ per mode, see Mode.Kind.
func (d DeviceInfo) Kind() Kind {
	var k Kind
	switch d.Position {
	case PositionFront:
		k |= KindFront
	case PositionBack:
		k |= KindBack
	case PositionExternal:
		k |= KindExternal
	}
	switch d.Lens {
	case LensWide:
		k |= KindWide
	case LensUltraWide:
		k |= KindUltraWide
	case LensTelephoto:
		k |= KindTelephoto
	}
	if d.Virtual {
		k |= KindVirtual
	} else {
		k |= KindPhysical
	}
	return k
}

// Is reports whether the camera has every device trait of k and a mode
// with every mode trait of k (always true for 0).
func (d DeviceInfo) Is(k Kind) bool {
	if dk := k & DeviceKinds; d.Kind()&dk != dk {
		return false
	}
	if k&ModeKinds == 0 {
		return true
	}
	for _, m := range d.Modes {
		if m.Is(k) {
			return true
		}
	}
	return false
}

// Kind returns the resolution class and aspect ratio traits of the mode.
func (m Mode) Kind() Kind {
	if m.Width <= 0 || m.Height <= 0 {
		return 0
	}
	var k Kind
	switch px := m.Width * m.Height; {
	case px <= lowMaxPixels:
		k |= KindLow
	case px <= midMaxPixels:
		k |= KindMid
	default:
		k |= KindHigh
	}
	long, short := float64(max(m.Width, m.Height)), float64(min(m.Width, m.Height))
	near := func(r float64) bool { return math.Abs(long/short-r) <= 0.02*r }
	switch {
	case near(1):
		k |= KindSquare
	case m.Width > m.Height:
		k |= KindLandscape
	default:
		k |= KindPortrait
	}
	if near(4.0 / 3) {
		k |= KindR4_3
	}
	if near(16.0 / 9) {
		k |= KindR16_9
	}
	return k
}

// Is reports whether the mode has every mode trait of k; device traits
// in k are ignored.
func (m Mode) Is(k Kind) bool { mk := k & ModeKinds; return m.Kind()&mk == mk }

// Rank orders devices for automatic choice: the default physical camera,
// then built-in ones, then other physical ones, then virtual ones.
func Rank(d DeviceInfo) int {
	r := 0
	if d.Virtual {
		r += 100
	}
	switch {
	case d.Default:
	case d.Position == PositionFront || d.Position == PositionBack:
		r += 10
	default:
		r += 20
	}
	return r
}

// SortDevices orders devices by preference, keeping the backend's order
// among equals.
func SortDevices(ds []DeviceInfo) {
	sort.SliceStable(ds, func(i, j int) bool { return Rank(ds[i]) < Rank(ds[j]) })
}

// Config is what the caller asks for. Zero fields mean "pick for me".
type Config struct {
	Width, Height int
	FrameRate     float64
	// Format is the output format the caller wants. The backend may deliver
	// another one (see Stream.Format); the public package converts.
	Format PixelFormat
	// Native asks the backend for exactly this native format when set
	// (MJPEG, YUYV, ...), bypassing the automatic choice.
	Native PixelFormat
	// Buffers is the number of driver buffers in flight (V4L2), 0 for the
	// backend default.
	Buffers int
	// Kind restricts which camera the public Open picks; backends ignore
	// it.
	Kind Kind
}

// Frame is one captured picture.
type Frame struct {
	Width   int
	Height  int
	Format  PixelFormat
	Planes  [][]byte
	Strides []int
	// Timestamp is the capture time relative to the first frame of the
	// stream (monotonic, from the device clock where the OS provides one).
	Timestamp time.Duration
	// Sequence counts frames delivered by the device; gaps mean dropped frames.
	Sequence uint64

	release  func()
	released uint32
}

// Release hands the frame memory back to the backend (for V4L2, re-queues
// the mmap'ed buffer). The frame must not be used afterwards. Idempotent and
// safe from several goroutines; a no-op for frames whose memory is Go-owned.
func (f *Frame) Release() {
	if f == nil || f.release == nil {
		return
	}
	if !atomic.CompareAndSwapUint32(&f.released, 0, 1) {
		return
	}
	f.release()
	f.Planes = nil
	f.Strides = nil
}

// SetRelease installs the release callback. Backends call it.
func SetRelease(f *Frame, fn func()) { f.release = fn }

// Owned reports whether the frame's memory belongs to Go (Release is a no-op).
func (f *Frame) Owned() bool { return f.release == nil }

// OpaqueAlpha sets the alpha byte of every pixel of an RGBA or BGRA frame
// to 255. Backends call it on the frames they deliver: cameras send
// X8R8G8B8-style formats whose fourth byte is padding (V4L2 XB24/XR24,
// Media Foundation RGB32) or an alpha of 0 (vivid's AB24), and image/png,
// Ebitengine and other premultiplied-alpha consumers would draw those
// pixels transparent.
func OpaqueAlpha(f *Frame) {
	if f.Format != RGBA && f.Format != BGRA {
		return
	}
	p, stride := f.Planes[0], f.Strides[0]
	for y := 0; y < f.Height; y++ {
		row := p[y*stride:]
		for x := 3; x < 4*f.Width; x += 4 {
			row[x] = 255
		}
	}
}

// Stream is an open, running camera.
type Stream interface {
	// Mode is the native mode the device was configured with. The frame
	// rate holds the one in effect (0 when unknown).
	Mode() Mode
	// Read blocks for the next frame. It returns ErrClosed after Close.
	Read(ctx context.Context) (*Frame, error)
	Close() error
}

// Backend is one operating-system camera API.
type Backend interface {
	Name() string
	// Available reports whether the API can be loaded on this machine.
	Available() bool
	Devices(ctx context.Context) ([]DeviceInfo, error)
	Open(ctx context.Context, id string, cfg Config) (Stream, error)
}

var (
	ErrNoDevice    = errors.New("hwcamera: no camera found")
	ErrNotFound    = errors.New("hwcamera: camera not found")
	ErrUnsupported = errors.New("hwcamera: unsupported")
	ErrClosed      = errors.New("hwcamera: closed")
	ErrPermission  = errors.New("hwcamera: camera access denied")
	ErrBusy        = errors.New("hwcamera: camera in use")
)

// BackendError wraps a native error code with the backend and call it came from.
type BackendError struct {
	Backend string
	Op      string
	Code    int64
	Err     error
}

func (e *BackendError) Error() string {
	s := fmt.Sprintf("hwcamera/%s: %s", e.Backend, e.Op)
	if e.Code != 0 {
		s += fmt.Sprintf(": code 0x%x", uint64(e.Code))
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *BackendError) Unwrap() error { return e.Err }

var (
	mu       sync.Mutex
	backends []Backend
)

// Register adds a backend; earlier registrations are preferred.
func Register(b Backend) {
	mu.Lock()
	defer mu.Unlock()
	backends = append(backends, b)
}

// Backends returns the registered backends in preference order.
func Backends() []Backend {
	mu.Lock()
	defer mu.Unlock()
	return append([]Backend(nil), backends...)
}

// ChooseMode picks the device mode that best matches cfg.
//
// Size: the exact size when offered, otherwise the smallest mode at least as
// large as asked, otherwise the largest. No size asked: 1280x720 if present,
// else the largest mode up to 1920x1080. Frame rate: modes reaching the asked
// rate win (30 fps when none asked). Format: cfg.Native when set; otherwise
// the cheapest to turn into cfg.Format (same format, then other raw formats,
// MJPEG last because it costs a JPEG decode on the CPU).
//
// The mode traits of cfg.Kind keep only the modes that have them (an
// error wrapping ErrNotFound when none does). Without a size, Low aims
// at 640x480 and Mid at 1280x720, or at 640 and 1280 pixels on the long
// side with a shape trait (an aspect ratio alone counts as Mid); High
// takes the largest mode that reaches the frame rate. Without a shape
// trait, landscape modes are preferred.
func ChooseMode(modes []Mode, cfg Config) (Mode, error) {
	if len(modes) == 0 {
		return Mode{}, ErrUnsupported
	}
	cand := modes
	if mk := cfg.Kind & ModeKinds; mk != 0 {
		var m []Mode
		for _, x := range modes {
			if x.Is(mk) {
				m = append(m, x)
			}
		}
		if len(m) == 0 {
			return Mode{}, fmt.Errorf("%w: no %s mode", ErrNotFound, mk)
		}
		cand = m
	}
	if cfg.Native != Unknown {
		var m []Mode
		for _, x := range modes {
			if x.Format == cfg.Native {
				m = append(m, x)
			}
		}
		if len(m) == 0 {
			return Mode{}, fmt.Errorf("%w: native format %s not offered", ErrUnsupported, cfg.Native)
		}
		cand = m
	}
	// No shape asked: landscape modes only, so that the square and
	// portrait modes of cameras like the MacBook's (1552x1552, 1080x1920
	// for Center Stage) are not picked by size alone. A size that is
	// square or portrait asks for that shape; a camera without landscape
	// modes keeps all.
	if cfg.Kind&ShapeKinds == 0 && !(cfg.Width > 0 && cfg.Height >= cfg.Width) {
		var m []Mode
		for _, x := range cand {
			if x.Width > x.Height {
				m = append(m, x)
			}
		}
		if len(m) > 0 {
			cand = m
		}
	}
	wantW, wantH := cfg.Width, cfg.Height
	wantFPS := cfg.FrameRate
	if wantFPS == 0 {
		wantFPS = 30
	}
	if wantW == 0 && wantH == 0 {
		low := cfg.Kind&KindLow != 0
		switch {
		case cfg.Kind&KindHigh != 0:
			wantW, wantH = largest(cand, wantFPS)
		case cfg.Kind&ShapeKinds == 0 && low:
			wantW, wantH = 640, 480
		case cfg.Kind&ShapeKinds == 0:
			wantW, wantH = 1280, 720
		default:
			// The long side; the other follows each mode's shape.
			long := 1280
			if low {
				long = 640
			}
			if cfg.Kind&KindPortrait != 0 {
				wantH = long
			} else {
				wantW = long
			}
		}
	}

	type scored struct {
		m     Mode
		score [4]int64 // lower is better, compared lexicographically
	}
	var all []scored
	for _, m := range cand {
		var s scored
		s.m = m
		// size
		w, h := wantW, wantH
		if w == 0 {
			w = h * m.Width / max(m.Height, 1)
		}
		if h == 0 {
			h = w * m.Height / max(m.Width, 1)
		}
		switch {
		case m.Width == w && m.Height == h:
			s.score[0] = 0
		case m.Width >= w && m.Height >= h:
			s.score[0] = 1_000_000 + int64(m.Width*m.Height-w*h)
		default:
			s.score[0] = 1_000_000_000_000 - int64(m.Width*m.Height)
		}
		// frame rate
		if fr := m.MaxFrameRate(); fr != 0 && fr+0.5 < wantFPS {
			s.score[1] = int64((wantFPS - fr) * 1000)
		}
		// format cost
		s.score[2] = formatCost(m.Format, cfg.Format)
		all = append(all, s)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].score, all[j].score
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
	return all[0].m, nil
}

// largest returns the size of the largest mode that reaches fps, or of
// the largest mode when none does.
func largest(ms []Mode, fps float64) (w, h int) {
	best, bestFast := -1, -1
	for i, m := range ms {
		if best < 0 || m.Width*m.Height > ms[best].Width*ms[best].Height {
			best = i
		}
		if fr := m.MaxFrameRate(); fr == 0 || fr+0.5 >= fps {
			if bestFast < 0 || m.Width*m.Height > ms[bestFast].Width*ms[bestFast].Height {
				bestFast = i
			}
		}
	}
	if bestFast >= 0 {
		best = bestFast
	}
	return ms[best].Width, ms[best].Height
}

func formatCost(native, want PixelFormat) int64 {
	if native == want {
		return 0
	}
	switch native {
	case NV12:
		return 1
	case YUYV, UYVY, I420:
		return 2
	case BGRA, RGBA:
		if want == RGBA || want == BGRA {
			return 1
		}
		return 3
	case MJPEG:
		return 10
	}
	return 5
}

// PickFrameRate returns the mode's frame rate closest to want (30 when 0),
// preferring rates at or above it, or 0 when the mode lists none.
func PickFrameRate(m Mode, want float64) float64 {
	if len(m.FrameRates) == 0 {
		return want
	}
	if want == 0 {
		want = 30
	}
	best := m.FrameRates[0]
	bestD := 1e18
	for _, r := range m.FrameRates {
		d := r - want
		if d < 0 {
			d = -d * 1000 // below the request is much worse
		}
		if d < bestD {
			best, bestD = r, d
		}
	}
	return best
}

// SortModes orders modes by format, then size descending.
func SortModes(ms []Mode) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Format != ms[j].Format {
			return ms[i].Format < ms[j].Format
		}
		if ms[i].Width*ms[i].Height != ms[j].Width*ms[j].Height {
			return ms[i].Width*ms[i].Height > ms[j].Width*ms[j].Height
		}
		return ms[i].Width > ms[j].Width
	})
	for _, m := range ms {
		sort.Sort(sort.Reverse(sort.Float64Slice(m.FrameRates)))
	}
}

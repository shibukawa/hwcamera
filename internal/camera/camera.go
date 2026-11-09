// Package camera holds the types shared by the public hwcamera package and
// the operating-system backends, so the backends can live in internal
// packages without importing the public one.
package camera

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	Modes    []Mode
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
func ChooseMode(modes []Mode, cfg Config) (Mode, error) {
	if len(modes) == 0 {
		return Mode{}, ErrUnsupported
	}
	cand := modes
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
	wantW, wantH := cfg.Width, cfg.Height
	if wantW == 0 && wantH == 0 {
		wantW, wantH = 1280, 720
	}
	wantFPS := cfg.FrameRate
	if wantFPS == 0 {
		wantFPS = 30
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

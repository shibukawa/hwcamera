// Package hwcamera captures video from the computer's cameras without cgo.
//
// It talks to each operating system's own camera API at run time: V4L2
// through ioctl on Linux, AVFoundation through the Objective-C runtime
// (purego) on macOS, and Media Foundation through raw COM vtable calls on
// Windows. CGO_ENABLED=0 builds work and cross-compile from one machine.
//
// Frames come out as NV12 by default, the input format of the hardware
// encoders of github.com/shibukawa/hwmediacodec, so a camera can be wired
// straight into an encoder (see the mediacodec module). The tensor package
// turns frames into the input tensors of a neural network for
// github.com/shibukawa/hwnpu or any other runtime.
//
//	cam, err := hwcamera.Open(ctx, "", hwcamera.WithSize(1280, 720), hwcamera.WithFrameRate(30))
//	if err != nil { ... }
//	defer cam.Close()
//	for {
//		f, err := cam.Read(ctx)
//		if err != nil { ... }
//		use(f.Planes[0], f.Planes[1]) // NV12
//		f.Release()
//	}
package hwcamera

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/pixconv"
)

type (
	PixelFormat  = camera.PixelFormat
	Position     = camera.Position
	Mode         = camera.Mode
	DeviceInfo   = camera.DeviceInfo
	Frame        = camera.Frame
	BackendError = camera.BackendError
)

const (
	Any   = camera.Unknown
	NV12  = camera.NV12
	I420  = camera.I420
	YUYV  = camera.YUYV
	UYVY  = camera.UYVY
	RGBA  = camera.RGBA
	BGRA  = camera.BGRA
	MJPEG = camera.MJPEG

	PositionUnknown  = camera.PositionUnknown
	PositionFront    = camera.PositionFront
	PositionBack     = camera.PositionBack
	PositionExternal = camera.PositionExternal
)

var (
	ErrNoDevice    = camera.ErrNoDevice
	ErrNotFound    = camera.ErrNotFound
	ErrUnsupported = camera.ErrUnsupported
	ErrClosed      = camera.ErrClosed
	ErrPermission  = camera.ErrPermission
	ErrBusy        = camera.ErrBusy
)

// backendsFor returns the registered backends that are available, filtered
// by the HWCAMERA_BACKEND environment variable (a comma-separated list of
// backend names) when it is set.
func backendsFor() []camera.Backend {
	var list []string
	if s := os.Getenv("HWCAMERA_BACKEND"); s != "" {
		list = strings.Split(s, ",")
	}
	var out []camera.Backend
	for _, b := range camera.Backends() {
		if len(list) > 0 {
			ok := false
			for _, n := range list {
				if strings.EqualFold(strings.TrimSpace(n), b.Name()) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		if b.Available() {
			out = append(out, b)
		}
	}
	return out
}

// Devices lists the cameras of every available backend.
func Devices(ctx context.Context) ([]DeviceInfo, error) {
	bs := backendsFor()
	if len(bs) == 0 {
		return nil, fmt.Errorf("%w: no camera API on this platform", ErrUnsupported)
	}
	var all []DeviceInfo
	var errs []error
	for _, b := range bs {
		ds, err := b.Devices(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		all = append(all, ds...)
	}
	if len(all) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	camera.SortDevices(all)
	return all, nil
}

// Option configures Open.
type Option func(*camera.Config)

// WithSize asks for a capture size. The device's closest mode is used
// (see Camera.Mode); frames are not scaled. Either value may be 0 to keep
// the aspect ratio free.
func WithSize(width, height int) Option {
	return func(c *camera.Config) { c.Width, c.Height = width, height }
}

// WithFrameRate asks for a frame rate in frames per second (default 30).
func WithFrameRate(fps float64) Option { return func(c *camera.Config) { c.FrameRate = fps } }

// WithFormat sets the format of the frames Read returns: NV12 (default),
// I420, YUYV, RGBA, BGRA, or Any for whatever the device delivers natively
// (cheapest, no conversion; may be MJPEG).
func WithFormat(f PixelFormat) Option { return func(c *camera.Config) { c.Format = f } }

// WithNativeFormat forces the device's native format (MJPEG or YUYV on most
// USB cameras) instead of letting the library choose. Useful to reach high
// resolutions or rates that a USB 2 camera offers only as MJPEG.
func WithNativeFormat(f PixelFormat) Option { return func(c *camera.Config) { c.Native = f } }

// WithBuffers sets the number of frames the driver may hold in flight (V4L2;
// default 4). Frames not released block the driver once all are out.
func WithBuffers(n int) Option { return func(c *camera.Config) { c.Buffers = n } }

// Camera is an open camera producing frames.
type Camera struct {
	info   DeviceInfo
	stream camera.Stream
	out    PixelFormat
	conv   *pixconv.Converter

	mu     sync.Mutex
	closed bool
}

// Open opens a camera by DeviceInfo.ID and starts capturing. With an empty
// id it picks one in the order of Devices: the system's default camera,
// built-in cameras, other physical cameras, and a virtual camera only when
// there is no physical one. A camera that fails to open is skipped.
func Open(ctx context.Context, id string, opts ...Option) (*Camera, error) {
	cfg := camera.Config{Format: NV12}
	for _, o := range opts {
		o(&cfg)
	}
	bs := backendsFor()
	if len(bs) == 0 {
		return nil, fmt.Errorf("%w: no camera API on this platform", ErrUnsupported)
	}
	type cand struct {
		b camera.Backend
		d DeviceInfo
	}
	var cands []cand
	var errs []error
	for _, b := range bs {
		ds, err := b.Devices(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, d := range ds {
			if id == "" || d.ID == id {
				cands = append(cands, cand{b, d})
			}
		}
	}
	if id == "" {
		sort.SliceStable(cands, func(i, j int) bool { return camera.Rank(cands[i].d) < camera.Rank(cands[j].d) })
		physical := false
		for _, c := range cands {
			physical = physical || !c.d.Virtual
		}
		if physical {
			kept := cands[:0]
			for _, c := range cands {
				if !c.d.Virtual {
					kept = append(kept, c)
				}
			}
			cands = kept
		}
	}
	for _, c := range cands {
		b, d := c.b, c.d
		s, err := b.Open(ctx, d.ID, cfg)
		if err != nil {
			if id != "" {
				return nil, err
			}
			errs = append(errs, err)
			continue
		}
		c := &Camera{info: d, stream: s, out: cfg.Format}
		if c.out == Any {
			c.out = s.Mode().Format
		}
		if s.Mode().Format != c.out {
			conv, err := pixconv.NewConverter(s.Mode().Format, c.out)
			if err != nil {
				s.Close()
				return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
			}
			c.conv = conv
		}
		return c, nil
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if id != "" {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil, ErrNoDevice
}

// Info describes the opened device.
func (c *Camera) Info() DeviceInfo { return c.info }

// Mode is the native capture mode in effect.
func (c *Camera) Mode() Mode { return c.stream.Mode() }

// Format is the format of the frames Read returns.
func (c *Camera) Format() PixelFormat { return c.out }

// Size is the frame size.
func (c *Camera) Size() (width, height int) {
	m := c.stream.Mode()
	return m.Width, m.Height
}

// Read blocks for the next frame. Call Frame.Release when done with it:
// frames in the device's native format may live in driver memory, and the
// driver stalls when every buffer is held. Converted frames are in Go
// memory, and Release is then a no-op.
//
// Read is safe for one goroutine at a time.
func (c *Camera) Read(ctx context.Context) (*Frame, error) {
	f, err := c.stream.Read(ctx)
	if err != nil {
		return nil, err
	}
	if c.conv == nil {
		return f, nil
	}
	out, err := c.conv.Convert(f)
	f.Release()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Close stops capturing and releases the device. Frames already returned
// must be released before or after; their memory stays valid until then.
func (c *Camera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.stream.Close()
}

// Backends returns the names of the camera APIs available on this machine,
// in preference order.
func Backends() []string {
	var n []string
	for _, b := range backendsFor() {
		n = append(n, b.Name())
	}
	return n
}

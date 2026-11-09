// Package audio holds the types shared by the public audio package and the
// operating-system audio capture backends.
package audio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SampleFormat is the encoding of one sample.
type SampleFormat int

const (
	// S16 is signed 16-bit little-endian PCM, interleaved.
	S16 SampleFormat = iota + 1
	// F32 is 32-bit float PCM in [-1, 1], little-endian, interleaved.
	F32
)

func (f SampleFormat) String() string {
	switch f {
	case S16:
		return "s16"
	case F32:
		return "f32"
	}
	return fmt.Sprintf("SampleFormat(%d)", int(f))
}

// Size returns the bytes per sample.
func (f SampleFormat) Size() int {
	if f == F32 {
		return 4
	}
	return 2
}

// DeviceInfo describes one audio input.
type DeviceInfo struct {
	// ID is the backend's identifier: the PulseAudio source name on Linux,
	// the Core Audio device UID on macOS, the endpoint ID on Windows.
	ID      string
	Name    string
	Backend string
	// Default reports whether this is the system's default input.
	Default bool
	// Monitor reports a loopback of an output (what the speakers play)
	// rather than a microphone.
	Monitor bool
	// SampleRate and Channels are the device's native format, 0 when unknown.
	SampleRate int
	Channels   int
}

// Config is what the caller asks for. The backend delivers exactly this
// format, converting in the OS audio stack where the device differs.
type Config struct {
	SampleRate int           // default 48000
	Channels   int           // default 1
	Format     SampleFormat  // default S16
	Period     time.Duration // samples per Read; default 20 ms
}

// Normalize fills the defaults.
func (c *Config) Normalize() {
	if c.SampleRate <= 0 {
		c.SampleRate = 48000
	}
	if c.Channels <= 0 {
		c.Channels = 1
	}
	if c.Format == 0 {
		c.Format = S16
	}
	if c.Period <= 0 {
		c.Period = 20 * time.Millisecond
	}
}

// PeriodFrames returns the frames (samples per channel) of one period.
func (c Config) PeriodFrames() int {
	n := int(int64(c.SampleRate) * int64(c.Period) / int64(time.Second))
	return max(n, 1)
}

// Buffer is one block of captured audio. Its memory belongs to the caller.
type Buffer struct {
	Data       []byte // interleaved samples
	Format     SampleFormat
	SampleRate int
	Channels   int
	Frames     int // samples per channel in Data
	// Timestamp is the capture time of the first frame, relative to the
	// first buffer of the stream.
	Timestamp time.Duration
	// Discontinuity reports that audio was lost before this buffer (the
	// reader fell behind or the device glitched).
	Discontinuity bool
}

// Stream is an open, running audio input.
type Stream interface {
	Config() Config // the format actually delivered
	Read(ctx context.Context) (*Buffer, error)
	Close() error
}

// Backend is one operating-system audio capture API.
type Backend interface {
	Name() string
	Available() bool
	Devices(ctx context.Context) ([]DeviceInfo, error)
	// Open opens the device with the given ID, or the default input for "".
	Open(ctx context.Context, id string, cfg Config) (Stream, error)
}

var (
	ErrNoDevice    = errors.New("hwcamera/audio: no audio input found")
	ErrNotFound    = errors.New("hwcamera/audio: audio input not found")
	ErrUnsupported = errors.New("hwcamera/audio: unsupported")
	ErrClosed      = errors.New("hwcamera/audio: closed")
	ErrPermission  = errors.New("hwcamera/audio: microphone access denied")
	ErrBusy        = errors.New("hwcamera/audio: audio input in use")
)

// BackendError wraps a native error with the backend and call it came from.
type BackendError struct {
	Backend string
	Op      string
	Code    int64
	Err     error
}

func (e *BackendError) Error() string {
	s := fmt.Sprintf("hwcamera/audio/%s: %s", e.Backend, e.Op)
	if e.Code != 0 {
		s += fmt.Sprintf(": code %d (0x%x)", e.Code, uint64(e.Code))
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

// Ring is a bounded queue of buffers for backends whose OS delivers audio
// on its own thread: Push drops the oldest buffer when full and marks the
// next one delivered as a discontinuity.
type Ring struct {
	mu   sync.Mutex
	cond chan struct{}
	q    []*Buffer
	max  int
	err  error
}

// NewRing returns a ring holding at most n buffers.
func NewRing(n int) *Ring { return &Ring{cond: make(chan struct{}, 1), max: n} }

// Push queues b (called from the OS callback; never blocks).
func (r *Ring) Push(b *Buffer) {
	r.mu.Lock()
	if r.err != nil {
		r.mu.Unlock()
		return
	}
	if len(r.q) >= r.max {
		// The oldest buffer is lost: whatever follows it is discontinuous.
		r.q = r.q[1:]
		if len(r.q) > 0 {
			r.q[0].Discontinuity = true
		} else {
			b.Discontinuity = true
		}
	}
	r.q = append(r.q, b)
	r.mu.Unlock()
	select {
	case r.cond <- struct{}{}:
	default:
	}
}

// Fail makes Pop return err once the queue is drained (device lost, closed).
func (r *Ring) Fail(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	select {
	case r.cond <- struct{}{}:
	default:
	}
}

// Pop blocks for the next buffer.
func (r *Ring) Pop(ctx context.Context) (*Buffer, error) {
	for {
		r.mu.Lock()
		if len(r.q) > 0 {
			b := r.q[0]
			r.q = r.q[1:]
			r.mu.Unlock()
			return b, nil
		}
		err := r.err
		r.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.cond:
		}
	}
}

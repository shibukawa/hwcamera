// Package audio captures sound from the computer's microphones without cgo,
// through each operating system's own audio API: PulseAudio (or PipeWire's
// PulseAudio server) on Linux, Core Audio's AudioQueue on macOS and WASAPI on
// Windows.
//
// Samples come out in the format asked for (48 kHz mono 16-bit by default);
// the OS audio stack resamples and mixes channels, so Read never returns
// anything else:
//
//	mic, err := audio.Open(ctx, "") // the default input
//	if err != nil { ... }
//	defer mic.Close()
//	for {
//		b, err := mic.Read(ctx) // 20 ms per buffer by default
//		if err != nil { ... }
//		use(b.S16())
//	}
package audio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unsafe"

	iaudio "github.com/shibukawa/hwcamera/internal/audio"
)

type (
	SampleFormat = iaudio.SampleFormat
	DeviceInfo   = iaudio.DeviceInfo
	BackendError = iaudio.BackendError
)

const (
	S16 = iaudio.S16
	F32 = iaudio.F32
)

var (
	ErrNoDevice    = iaudio.ErrNoDevice
	ErrNotFound    = iaudio.ErrNotFound
	ErrUnsupported = iaudio.ErrUnsupported
	ErrClosed      = iaudio.ErrClosed
	ErrPermission  = iaudio.ErrPermission
	ErrBusy        = iaudio.ErrBusy
)

// Buffer is one block of captured audio, owned by the caller.
type Buffer iaudio.Buffer

// S16 returns the samples of an S16 buffer as int16 values sharing its
// memory (nil for another format).
func (b *Buffer) S16() []int16 {
	if b.Format != S16 || len(b.Data) < 2 {
		return nil
	}
	return unsafe.Slice((*int16)(unsafe.Pointer(&b.Data[0])), len(b.Data)/2)
}

// F32 returns the samples of an F32 buffer as float32 values sharing its
// memory (nil for another format).
func (b *Buffer) F32() []float32 {
	if b.Format != F32 || len(b.Data) < 4 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b.Data[0])), len(b.Data)/4)
}

// Duration is the length of the buffer.
func (b *Buffer) Duration() time.Duration {
	if b.SampleRate == 0 {
		return 0
	}
	return time.Duration(b.Frames) * time.Second / time.Duration(b.SampleRate)
}

// Level returns the peak and RMS level of the buffer in [0, 1].
func (b *Buffer) Level() (peak, rms float64) {
	var sum float64
	n := 0
	switch b.Format {
	case S16:
		for _, v := range b.S16() {
			x := float64(v) / 32768
			sum += x * x
			peak = max(peak, max(x, -x))
			n++
		}
	case F32:
		for _, v := range b.F32() {
			x := float64(v)
			sum += x * x
			peak = max(peak, max(x, -x))
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return min(peak, 1), math.Sqrt(sum / float64(n))
}

func backendsFor() []iaudio.Backend {
	var list []string
	if s := os.Getenv("HWCAMERA_AUDIO_BACKEND"); s != "" {
		list = strings.Split(s, ",")
	}
	var out []iaudio.Backend
	for _, b := range iaudio.Backends() {
		if len(list) > 0 {
			ok := false
			for _, n := range list {
				ok = ok || strings.EqualFold(strings.TrimSpace(n), b.Name())
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

// Backends returns the names of the audio APIs available on this machine.
func Backends() []string {
	var n []string
	for _, b := range backendsFor() {
		n = append(n, b.Name())
	}
	return n
}

// Devices lists the audio inputs. On Linux the list also has the monitors
// of the outputs (DeviceInfo.Monitor): recording one records what the
// speakers play.
func Devices(ctx context.Context) ([]DeviceInfo, error) {
	bs := backendsFor()
	if len(bs) == 0 {
		return nil, fmt.Errorf("%w: no audio capture API on this machine", ErrUnsupported)
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
	return all, nil
}

// Option configures Open.
type Option func(*iaudio.Config)

// WithSampleRate sets the sample rate in Hz (default 48000).
func WithSampleRate(hz int) Option { return func(c *iaudio.Config) { c.SampleRate = hz } }

// WithChannels sets the channel count (default 1).
func WithChannels(n int) Option { return func(c *iaudio.Config) { c.Channels = n } }

// WithFormat sets the sample format, S16 (default) or F32.
func WithFormat(f SampleFormat) Option { return func(c *iaudio.Config) { c.Format = f } }

// WithPeriod sets the length of the buffers Read returns (default 20 ms,
// which is also one Opus packet). Shorter periods lower the latency and
// raise the CPU wake-ups.
func WithPeriod(d time.Duration) Option { return func(c *iaudio.Config) { c.Period = d } }

// Mic is an open audio input.
type Mic struct {
	info   DeviceInfo
	stream iaudio.Stream
}

// Open opens the input with the given DeviceInfo.ID, or the system's
// default input for "", and starts capturing.
func Open(ctx context.Context, id string, opts ...Option) (*Mic, error) {
	var cfg iaudio.Config
	for _, o := range opts {
		o(&cfg)
	}
	cfg.Normalize()
	if cfg.Format != S16 && cfg.Format != F32 {
		return nil, fmt.Errorf("%w: sample format %v", ErrUnsupported, cfg.Format)
	}
	bs := backendsFor()
	if len(bs) == 0 {
		return nil, fmt.Errorf("%w: no audio capture API on this machine", ErrUnsupported)
	}
	var errs []error
	for _, b := range bs {
		s, err := b.Open(ctx, id, cfg)
		if err != nil {
			if errors.Is(err, ErrNotFound) && len(bs) > 1 {
				errs = append(errs, err)
				continue
			}
			return nil, err
		}
		m := &Mic{stream: s, info: DeviceInfo{ID: id, Backend: b.Name()}}
		// Fill the name from the device list; cheap on every backend.
		if ds, err := b.Devices(ctx); err == nil {
			for _, d := range ds {
				if d.ID == id || (id == "" && d.Default) {
					m.info = d
					break
				}
			}
		}
		return m, nil
	}
	return nil, errors.Join(errs...)
}

// Info describes the opened input (ID empty and Name unknown when the
// default input was opened and the backend could not tell which it is).
func (m *Mic) Info() DeviceInfo { return m.info }

// SampleRate, Channels and Format describe the samples Read returns.
func (m *Mic) SampleRate() int       { return m.stream.Config().SampleRate }
func (m *Mic) Channels() int         { return m.stream.Config().Channels }
func (m *Mic) Format() SampleFormat  { return m.stream.Config().Format }
func (m *Mic) Period() time.Duration { return m.stream.Config().Period }

// Read blocks for the next buffer. Buffers that were not read in time are
// dropped (about one second is kept), and the next buffer then has
// Discontinuity set. Read is safe for one goroutine at a time.
func (m *Mic) Read(ctx context.Context) (*Buffer, error) {
	b, err := m.stream.Read(ctx)
	if err != nil {
		return nil, err
	}
	return (*Buffer)(b), nil
}

// Close stops capturing. A blocked Read returns ErrClosed.
func (m *Mic) Close() error { return m.stream.Close() }

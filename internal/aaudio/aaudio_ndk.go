//go:build android || hwcamera_typecheck

package aaudio

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	iaudio "github.com/shibukawa/hwcamera/internal/audio"
)

// Bound functions of libaaudio.so (all API 26). Streams and builders
// travel as uintptr.
var (
	AAudio_createStreamBuilder             func(builder *uintptr) int32
	AAudio_convertResultToText             func(r int32) string
	AAudioStreamBuilder_setDeviceId        func(b uintptr, id int32)
	AAudioStreamBuilder_setDirection       func(b uintptr, dir int32)
	AAudioStreamBuilder_setSampleRate      func(b uintptr, rate int32)
	AAudioStreamBuilder_setChannelCount    func(b uintptr, n int32)
	AAudioStreamBuilder_setFormat          func(b uintptr, f int32)
	AAudioStreamBuilder_setSharingMode     func(b uintptr, m int32)
	AAudioStreamBuilder_setPerformanceMode func(b uintptr, m int32)
	AAudioStreamBuilder_openStream         func(b uintptr, s *uintptr) int32
	AAudioStreamBuilder_delete             func(b uintptr) int32
	AAudioStream_requestStart              func(s uintptr) int32
	AAudioStream_requestStop               func(s uintptr) int32
	AAudioStream_close                     func(s uintptr) int32
	AAudioStream_read                      func(s uintptr, buf unsafe.Pointer, frames int32, timeoutNanos int64) int32
	AAudioStream_getSampleRate             func(s uintptr) int32
	AAudioStream_getChannelCount           func(s uintptr) int32
	AAudioStream_getFormat                 func(s uintptr) int32
	AAudioStream_getXRunCount              func(s uintptr) int32
)

var (
	loadOnce sync.Once
	loadErr  error
)

func load() error {
	loadOnce.Do(func() {
		h, err := purego.Dlopen("libaaudio.so", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			loadErr = fmt.Errorf("aaudio: dlopen libaaudio.so (Android 8.0 / API 26 or later): %w", err)
			return
		}
		var missing []string
		bind := func(fptr any, name string) {
			addr, err := purego.Dlsym(h, name)
			if err != nil {
				missing = append(missing, name)
				return
			}
			purego.RegisterFunc(fptr, addr)
		}
		bind(&AAudio_createStreamBuilder, "AAudio_createStreamBuilder")
		bind(&AAudio_convertResultToText, "AAudio_convertResultToText")
		bind(&AAudioStreamBuilder_setDeviceId, "AAudioStreamBuilder_setDeviceId")
		bind(&AAudioStreamBuilder_setDirection, "AAudioStreamBuilder_setDirection")
		bind(&AAudioStreamBuilder_setSampleRate, "AAudioStreamBuilder_setSampleRate")
		bind(&AAudioStreamBuilder_setChannelCount, "AAudioStreamBuilder_setChannelCount")
		bind(&AAudioStreamBuilder_setFormat, "AAudioStreamBuilder_setFormat")
		bind(&AAudioStreamBuilder_setSharingMode, "AAudioStreamBuilder_setSharingMode")
		bind(&AAudioStreamBuilder_setPerformanceMode, "AAudioStreamBuilder_setPerformanceMode")
		bind(&AAudioStreamBuilder_openStream, "AAudioStreamBuilder_openStream")
		bind(&AAudioStreamBuilder_delete, "AAudioStreamBuilder_delete")
		bind(&AAudioStream_requestStart, "AAudioStream_requestStart")
		bind(&AAudioStream_requestStop, "AAudioStream_requestStop")
		bind(&AAudioStream_close, "AAudioStream_close")
		bind(&AAudioStream_read, "AAudioStream_read")
		bind(&AAudioStream_getSampleRate, "AAudioStream_getSampleRate")
		bind(&AAudioStream_getChannelCount, "AAudioStream_getChannelCount")
		bind(&AAudioStream_getFormat, "AAudioStream_getFormat")
		bind(&AAudioStream_getXRunCount, "AAudioStream_getXRunCount")
		if len(missing) > 0 {
			loadErr = errors.New("aaudio: libaaudio.so lacks " + strings.Join(missing, ", "))
		}
	})
	return loadErr
}

func errOf(op string, r int32) error {
	text := ""
	if AAudio_convertResultToText != nil {
		text = AAudio_convertResultToText(r)
	}
	return resultErr(op, r, text)
}

// Backend is the AAudio backend.
type Backend struct{}

func (Backend) Name() string { return Name }

func (Backend) Available() bool { return load() == nil }

// Devices returns the default input only: the NDK cannot enumerate audio
// devices (Java's AudioManager.getDevices can; pass an AudioDeviceInfo
// ID to Open).
func (Backend) Devices(ctx context.Context) ([]iaudio.DeviceInfo, error) {
	if err := load(); err != nil {
		return nil, fmt.Errorf("%w: %v", iaudio.ErrUnsupported, err)
	}
	return []iaudio.DeviceInfo{{ID: "", Name: "Default input", Backend: Name, Default: true}}, nil
}

// request is one attempt at opening a stream; zero fields are
// AAUDIO_UNSPECIFIED.
type request struct {
	device, rate, channels, format int32
}

func openWith(r request) (uintptr, int32) {
	var b uintptr
	if res := AAudio_createStreamBuilder(&b); res != resultOK {
		return 0, res
	}
	defer AAudioStreamBuilder_delete(b)
	AAudioStreamBuilder_setDirection(b, directionInput)
	AAudioStreamBuilder_setSharingMode(b, sharingShared)
	// NONE takes the normal (non-FAST) capture path, where the audio
	// server can resample to the requested rate.
	AAudioStreamBuilder_setPerformanceMode(b, performanceNone)
	if r.device != 0 {
		AAudioStreamBuilder_setDeviceId(b, r.device)
	}
	AAudioStreamBuilder_setSampleRate(b, r.rate)
	AAudioStreamBuilder_setChannelCount(b, r.channels)
	AAudioStreamBuilder_setFormat(b, r.format)
	var s uintptr
	if res := AAudioStreamBuilder_openStream(b, &s); res != resultOK {
		return 0, res
	}
	return s, resultOK
}

func (Backend) Open(ctx context.Context, id string, cfg iaudio.Config) (iaudio.Stream, error) {
	if err := load(); err != nil {
		return nil, fmt.Errorf("%w: %v", iaudio.ErrUnsupported, err)
	}
	cfg.Normalize()
	var dev int32
	if id != "" {
		n, err := strconv.ParseInt(id, 10, 32)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%w: %q (an AudioDeviceInfo ID in decimal, or \"\" for the default input)", iaudio.ErrNotFound, id)
		}
		dev = int32(n)
	}
	// The exact format first; then the device's own rate and channels,
	// then everything the device's own, converting in Go.
	attempts := []request{
		{dev, int32(cfg.SampleRate), int32(cfg.Channels), aaudioFormat(cfg.Format)},
		{dev, unspecified, unspecified, aaudioFormat(cfg.Format)},
		{dev, unspecified, unspecified, unspecified},
	}
	var h uintptr
	var first int32
	for i, r := range attempts {
		var res int32
		if h, res = openWith(r); res == resultOK {
			break
		}
		if i == 0 {
			first = res
		}
	}
	if h == 0 {
		err := errOf("AAudioStreamBuilder_openStream", first)
		hint := openHint
		if id != "" {
			hint += " is " + id + " an input's AudioDeviceInfo ID?"
		}
		return nil, fmt.Errorf("%w (%s)", err, hint)
	}
	conv, err := newConverter(int(AAudioStream_getSampleRate(h)), int(AAudioStream_getChannelCount(h)), AAudioStream_getFormat(h), cfg)
	if err != nil {
		AAudioStream_close(h)
		return nil, err
	}
	if res := AAudioStream_requestStart(h); res != resultOK {
		AAudioStream_close(h)
		return nil, fmt.Errorf("%w (%s)", errOf("AAudioStream_requestStart", res), openHint)
	}
	frameBytes := cfg.Channels * cfg.Format.Size()
	periods := max(int(time.Second/cfg.Period), 4)
	s := &stream{
		h: h, cfg: cfg, conv: conv,
		acc:        accumulator{period: cfg.PeriodFrames() * frameBytes},
		frameBytes: frameBytes,
		ring:       iaudio.NewRing(periods),
		done:       make(chan struct{}),
	}
	// One read covers about a period of input.
	s.chunk = max(conv.inRate*int(cfg.Period/time.Millisecond)/1000, 64)
	s.in = make([]byte, s.chunk*conv.inFrameBytes())
	s.xruns = AAudioStream_getXRunCount(h)
	go s.run()
	return s, nil
}

// readTimeout bounds one blocking AAudioStream_read, so Close is noticed.
const readTimeout = 100 * time.Millisecond

type stream struct {
	h          uintptr
	cfg        iaudio.Config
	conv       *converter
	acc        accumulator
	frameBytes int
	ring       *iaudio.Ring
	in         []byte
	chunk      int
	frames     int64
	xruns      int32
	gap        bool

	closing   atomic.Bool
	closeOnce sync.Once
	done      chan struct{}
}

func (s *stream) Config() iaudio.Config { return s.cfg }

// run reads on its own goroutine and owns the stream handle: it stops and
// closes it when told to quit, so no AAudio call overlaps a read.
func (s *stream) run() {
	defer close(s.done)
	defer func() {
		AAudioStream_requestStop(s.h)
		AAudioStream_close(s.h)
	}()
	for !s.closing.Load() {
		n := AAudioStream_read(s.h, unsafe.Pointer(&s.in[0]), int32(s.chunk), int64(readTimeout))
		if n == errTimeout || n == errWouldBlock {
			n = 0
		}
		if n < 0 {
			s.ring.Fail(fmt.Errorf("%w: %v", iaudio.ErrClosed, errOf("AAudioStream_read", n)))
			return
		}
		if x := AAudioStream_getXRunCount(s.h); x > s.xruns {
			s.xruns = x
			s.gap = true
		}
		if n == 0 {
			continue
		}
		s.acc.buf = s.conv.convert(s.acc.buf, s.in, int(n))
		for {
			p, ok := s.acc.next()
			if !ok {
				break
			}
			fr := len(p) / s.frameBytes
			s.ring.Push(&iaudio.Buffer{
				Data: p, Format: s.cfg.Format, SampleRate: s.cfg.SampleRate, Channels: s.cfg.Channels,
				Frames:        fr,
				Timestamp:     time.Duration(s.frames) * time.Second / time.Duration(s.cfg.SampleRate),
				Discontinuity: s.gap,
			})
			s.gap = false
			s.frames += int64(fr)
		}
	}
	s.ring.Fail(iaudio.ErrClosed)
}

func (s *stream) Read(ctx context.Context) (*iaudio.Buffer, error) {
	if s.closing.Load() {
		return nil, iaudio.ErrClosed
	}
	return s.ring.Pop(ctx)
}

// Close stops the reader (within one read timeout) and closes the stream.
func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		s.ring.Fail(iaudio.ErrClosed)
		<-s.done
	})
	return nil
}

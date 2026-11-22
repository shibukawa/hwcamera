// Package aaudio is the Android audio capture backend: AAudio
// (libaaudio.so, Android 8.0 / API 26), loaded at run time with purego. No
// cgo source is involved, but purego needs the cgo runtime on Android, so
// the app is built with CGO_ENABLED=1 and the NDK toolchain.
//
// The app must hold android.permission.RECORD_AUDIO, declared in its
// manifest and granted at run time from Java or Kotlin before Open.
//
// The NDK has no device enumeration (that is Java's AudioManager.
// getDevices), so Devices returns the default input only. Open accepts
// "" for it or the decimal AudioDeviceInfo.getId() of a specific input.
//
// The stream is opened in shared mode with the performance mode NONE,
// asking for exactly the caller's rate, channel count and sample format;
// on that path Android's audio server resamples and converts. When the
// device refuses, the stream is opened in its own format and the samples
// are converted in Go (I16 / I24 / I32 / float, channel down- or up-mix,
// linear-interpolation resampling), so Read always returns the format
// asked for.
//
// This file holds the constants and the pure-Go conversion, which compile
// everywhere so they can be tested on the desktop; the bindings
// (aaudio_ndk.go) build for android, or with the hwcamera_typecheck tag
// for type-checking without the NDK.
package aaudio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	iaudio "github.com/shibukawa/hwcamera/internal/audio"
)

// Name is the backend name.
const Name = "aaudio"

// aaudio/AAudio.h values.
const (
	directionInput = 1 // AAUDIO_DIRECTION_INPUT

	unspecified = 0 // AAUDIO_UNSPECIFIED

	formatI16       = 1 // AAUDIO_FORMAT_PCM_I16
	formatFloat     = 2 // AAUDIO_FORMAT_PCM_FLOAT
	formatI24Packed = 3 // AAUDIO_FORMAT_PCM_I24_PACKED (API 31)
	formatI32       = 4 // AAUDIO_FORMAT_PCM_I32 (API 31)

	sharingShared = 1 // AAUDIO_SHARING_MODE_SHARED

	performanceNone       = 10 // AAUDIO_PERFORMANCE_MODE_NONE
	performancePowerSave  = 11 // AAUDIO_PERFORMANCE_MODE_POWER_SAVING
	performanceLowLatency = 12 // AAUDIO_PERFORMANCE_MODE_LOW_LATENCY
)

// aaudio_result_t values.
const (
	resultOK           = 0
	errDisconnected    = -899
	errIllegalArgument = -898
	errInternal        = -896
	errInvalidState    = -895
	errInvalidHandle   = -892
	errUnimplemented   = -890
	errUnavailable     = -889
	errNoFreeHandles   = -888
	errNoMemory        = -887
	errNull            = -886
	errTimeout         = -885
	errWouldBlock      = -884
	errInvalidFormat   = -883
	errOutOfRange      = -882
	errNoService       = -881
	errInvalidRate     = -880
)

func resultName(r int32) string {
	switch r {
	case errDisconnected:
		return "AAUDIO_ERROR_DISCONNECTED"
	case errIllegalArgument:
		return "AAUDIO_ERROR_ILLEGAL_ARGUMENT"
	case errInternal:
		return "AAUDIO_ERROR_INTERNAL"
	case errInvalidState:
		return "AAUDIO_ERROR_INVALID_STATE"
	case errInvalidHandle:
		return "AAUDIO_ERROR_INVALID_HANDLE"
	case errUnimplemented:
		return "AAUDIO_ERROR_UNIMPLEMENTED"
	case errUnavailable:
		return "AAUDIO_ERROR_UNAVAILABLE"
	case errNoFreeHandles:
		return "AAUDIO_ERROR_NO_FREE_HANDLES"
	case errNoMemory:
		return "AAUDIO_ERROR_NO_MEMORY"
	case errNull:
		return "AAUDIO_ERROR_NULL"
	case errTimeout:
		return "AAUDIO_ERROR_TIMEOUT"
	case errWouldBlock:
		return "AAUDIO_ERROR_WOULD_BLOCK"
	case errInvalidFormat:
		return "AAUDIO_ERROR_INVALID_FORMAT"
	case errOutOfRange:
		return "AAUDIO_ERROR_OUT_OF_RANGE"
	case errNoService:
		return "AAUDIO_ERROR_NO_SERVICE"
	case errInvalidRate:
		return "AAUDIO_ERROR_INVALID_RATE"
	}
	return fmt.Sprintf("aaudio_result_t %d", r)
}

// resultErr wraps an aaudio_result_t; text is AAudio_convertResultToText
// when available.
func resultErr(op string, r int32, text string) error {
	if text == "" {
		text = resultName(r)
	}
	var err error
	switch r {
	case errDisconnected:
		err = fmt.Errorf("%w (%s: input disconnected)", iaudio.ErrClosed, text)
	case errNoFreeHandles, errUnavailable:
		err = fmt.Errorf("%w (%s)", iaudio.ErrBusy, text)
	default:
		err = errors.New(text)
	}
	return &iaudio.BackendError{Backend: Name, Op: op, Code: int64(r), Err: err}
}

// openHint is appended to open failures: AAudio reports a missing
// RECORD_AUDIO grant only as a generic failure.
const openHint = "does the app hold android.permission.RECORD_AUDIO, granted at run time?"

// sampleBytes returns the bytes per sample of an AAudio format, 0 for a
// format the converter does not read.
func sampleBytes(f int32) int {
	switch f {
	case formatI16:
		return 2
	case formatI24Packed:
		return 3
	case formatFloat, formatI32:
		return 4
	}
	return 0
}

func aaudioFormat(f iaudio.SampleFormat) int32 {
	if f == iaudio.F32 {
		return formatFloat
	}
	return formatI16
}

// converter turns AAudio input samples (any rate, channel count and PCM
// format) into the caller's format. It keeps state between calls so the
// resampler runs seamlessly across reads.
type converter struct {
	inRate, inCh int
	inFmt        int32
	out          iaudio.Config
	outFmt       int32

	// pos is the position of the next output frame in input frames from
	// hist[0], counted in 1/out.SampleRate units so that it stays exact
	// however the input is chunked; each output frame advances it by
	// inRate.
	pos  int64
	hist []float32 // the last input frame (out.Channels samples) from the previous call
	f    []float32 // scratch: mixed input
}

func newConverter(inRate, inCh int, inFmt int32, out iaudio.Config) (*converter, error) {
	if sampleBytes(inFmt) == 0 {
		return nil, fmt.Errorf("%w: AAudio sample format %d", iaudio.ErrUnsupported, inFmt)
	}
	if inRate <= 0 || inCh <= 0 {
		return nil, fmt.Errorf("%w: AAudio stream %d Hz, %d channels", iaudio.ErrUnsupported, inRate, inCh)
	}
	return &converter{
		inRate: inRate, inCh: inCh, inFmt: inFmt, out: out, outFmt: aaudioFormat(out.Format),
	}, nil
}

// identity reports that the input already is the output format.
func (c *converter) identity() bool {
	return c.inRate == c.out.SampleRate && c.inCh == c.out.Channels && c.inFmt == c.outFmt
}

// inFrameBytes is the size of one input frame.
func (c *converter) inFrameBytes() int { return c.inCh * sampleBytes(c.inFmt) }

func decode(b []byte, f int32) float32 {
	switch f {
	case formatI16:
		return float32(int16(binary.LittleEndian.Uint16(b))) / 32768
	case formatI24Packed:
		v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
		return float32(v) / (1 << 23)
	case formatI32:
		return float32(float64(int32(binary.LittleEndian.Uint32(b))) / (1 << 31))
	case formatFloat:
		return math.Float32frombits(binary.LittleEndian.Uint32(b))
	}
	return 0
}

// convert appends the output for frames input frames in to dst.
func (c *converter) convert(dst, in []byte, frames int) []byte {
	if c.identity() {
		return append(dst, in[:frames*c.inFrameBytes()]...)
	}
	oc := c.out.Channels
	sb := sampleBytes(c.inFmt)
	// Decode and mix into out.Channels channels, after the kept frame.
	f := append(c.f[:0], c.hist...)
	base := len(f)
	f = append(f, make([]float32, frames*oc)...)
	for i := 0; i < frames; i++ {
		fr := in[i*c.inCh*sb:]
		o := f[base+i*oc : base+(i+1)*oc]
		switch {
		case oc == 1 && c.inCh > 1:
			var sum float32
			for ch := 0; ch < c.inCh; ch++ {
				sum += decode(fr[ch*sb:], c.inFmt)
			}
			o[0] = sum / float32(c.inCh)
		default:
			for ch := range o {
				o[ch] = decode(fr[(ch%c.inCh)*sb:], c.inFmt)
			}
		}
	}
	c.f = f
	total := len(f) / oc
	if c.inRate == c.out.SampleRate {
		dst = c.encode(dst, f[base:])
		c.hist = c.hist[:0]
		return dst
	}
	// Linear interpolation between neighbouring input frames.
	var outS []float32
	den := int64(c.out.SampleRate)
	p := c.pos
	for p/den+1 < int64(total) {
		i := int(p / den)
		t := float32(float64(p%den) / float64(den))
		a, b := f[i*oc:(i+1)*oc], f[(i+1)*oc:(i+2)*oc]
		for ch := 0; ch < oc; ch++ {
			outS = append(outS, a[ch]+(b[ch]-a[ch])*t)
		}
		p += int64(c.inRate)
	}
	// Keep the last frame: the next output may fall between it and the
	// first frame of the next call.
	c.pos = p - int64(total-1)*den
	c.hist = append(c.hist[:0], f[(total-1)*oc:]...)
	return c.encode(dst, outS)
}

func (c *converter) encode(dst []byte, s []float32) []byte {
	for _, v := range s {
		if c.out.Format == iaudio.F32 {
			dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(v))
			continue
		}
		x := math.Round(float64(v) * 32768)
		x = max(min(x, 32767), -32768)
		dst = binary.LittleEndian.AppendUint16(dst, uint16(int16(x)))
	}
	return dst
}

// accumulator cuts a byte stream into periods.
type accumulator struct {
	period int
	buf    []byte
}

// next returns one full period when available.
func (a *accumulator) next() ([]byte, bool) {
	if len(a.buf) < a.period {
		return nil, false
	}
	p := make([]byte, a.period)
	copy(p, a.buf)
	n := copy(a.buf, a.buf[a.period:])
	a.buf = a.buf[:n]
	return p, true
}

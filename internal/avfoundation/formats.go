// Package avfoundation is the macOS camera backend. It drives AVFoundation
// through the Objective-C runtime with purego, so no cgo is needed.
//
// This file holds the platform-independent pieces (four-character code
// mapping, mode merging, frame duration arithmetic) so they can be unit
// tested on any machine.
package avfoundation

import (
	"math"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// Core Video / Core Media four-character codes.
const (
	fourCC420v uint32 = 0x34323076 // '420v' kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange
	fourCC420f uint32 = 0x34323066 // '420f' kCVPixelFormatType_420YpCbCr8BiPlanarFullRange
	fourCC2vuy uint32 = 0x32767579 // '2vuy' kCVPixelFormatType_422YpCbCr8
	fourCCyuvs uint32 = 0x79757673 // 'yuvs' kCVPixelFormatType_422YpCbCr8_yuvs
	fourCCBGRA uint32 = 0x42475241 // 'BGRA' kCVPixelFormatType_32BGRA
	fourCCjpeg uint32 = 0x6a706567 // 'jpeg' kCMVideoCodecType_JPEG
	fourCCdmb1 uint32 = 0x646d6231 // 'dmb1' kCMVideoCodecType_JPEG_OpenDML (UVC MJPEG)
)

// formatFromFourCC maps a format description media subtype or a pixel
// buffer format type to a PixelFormat; Unknown for anything else.
func formatFromFourCC(cc uint32) camera.PixelFormat {
	switch cc {
	case fourCC420v, fourCC420f:
		return camera.NV12
	case fourCC2vuy:
		return camera.UYVY
	case fourCCyuvs:
		return camera.YUYV
	case fourCCBGRA:
		return camera.BGRA
	case fourCCjpeg, fourCCdmb1:
		return camera.MJPEG
	}
	return camera.Unknown
}

// outputFourCC picks what AVCaptureVideoDataOutput is asked to deliver.
// The output converts in hardware from whatever the device produces
// (including decoding MJPEG), so only two targets are worth asking for:
// BGRA for callers that want RGB, NV12 for everything else because it is
// what the encoders want and what the public package converts from most
// cheaply.
func outputFourCC(want camera.PixelFormat) (uint32, camera.PixelFormat) {
	if want == camera.RGBA || want == camera.BGRA {
		return fourCCBGRA, camera.BGRA
	}
	return fourCC420v, camera.NV12
}

// frameRange is one AVFrameRateRange.
type frameRange struct {
	MinRate, MaxRate float64
}

// ratesOf lists the frame rates worth offering from a format's ranges:
// each range's maximum, 30 when a range covers it (the usual request) and
// each minimum.
func ratesOf(ranges []frameRange) []float64 {
	var out []float64
	for _, r := range ranges {
		out = addRate(out, r.MaxRate)
		if r.MinRate <= 30+rateEpsilon && r.MaxRate >= 30-rateEpsilon {
			out = addRate(out, 30)
		}
		out = addRate(out, r.MinRate)
	}
	return out
}

// rateEpsilon absorbs the rounding in AVFrameRateRange (a 30 fps range
// often reports 30.000030).
const rateEpsilon = 0.01

func addRate(rs []float64, r float64) []float64 {
	if r <= 0 {
		return rs
	}
	for _, x := range rs {
		if math.Abs(x-r) < rateEpsilon {
			return rs
		}
	}
	return append(rs, r)
}

type modeKey struct {
	f    camera.PixelFormat
	w, h int
}

// mergeModes folds modes with the same format and size into one (devices
// list '420v' and '420f', or several color spaces, as separate formats)
// and sorts the result.
func mergeModes(in []camera.Mode) []camera.Mode {
	idx := map[modeKey]int{}
	var out []camera.Mode
	for _, m := range in {
		if m.Format == camera.Unknown || m.Width <= 0 || m.Height <= 0 {
			continue
		}
		k := modeKey{m.Format, m.Width, m.Height}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, camera.Mode{Format: m.Format, Width: m.Width, Height: m.Height})
			i = len(out) - 1
		}
		for _, r := range m.FrameRates {
			out[i].FrameRates = addRate(out[i].FrameRates, r)
		}
	}
	camera.SortModes(out)
	return out
}

// cmTime mirrors CMTime (24 bytes: int64, int32, uint32, int64).
type cmTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

const cmTimeFlagsValid uint32 = 1

func (t cmTime) valid() bool { return t.Flags&cmTimeFlagsValid != 0 && t.Timescale > 0 }

func (t cmTime) seconds() float64 {
	if !t.valid() {
		return 0
	}
	return float64(t.Value) / float64(t.Timescale)
}

// durationFor returns the frame duration for rate. setActiveVideoMin/Max
// FrameDuration: raise an exception for durations outside the format's
// ranges, so a rate at a range's edge reuses the range's own CMTime
// (29.97 is 1001/30000, which no decimal rounding reproduces) instead of
// recomputing it.
func durationFor(rate float64, r frameRange, minDur, maxDur cmTime) cmTime {
	if math.Abs(rate-r.MaxRate) < rateEpsilon && minDur.valid() {
		return minDur
	}
	if math.Abs(rate-r.MinRate) < rateEpsilon && maxDur.valid() {
		return maxDur
	}
	if rate == math.Trunc(rate) {
		return cmTime{Value: 1, Timescale: int32(rate), Flags: cmTimeFlagsValid}
	}
	return cmTime{Value: 1000, Timescale: int32(math.Round(rate * 1000)), Flags: cmTimeFlagsValid}
}

// rangeCovers reports whether r supports rate.
func rangeCovers(r frameRange, rate float64) bool {
	return rate >= r.MinRate-rateEpsilon && rate <= r.MaxRate+rateEpsilon
}

// nanos converts the time to nanoseconds without overflowing for host
// clock values (timescale 1e9, value in the 1e15 range).
func (t cmTime) nanos() int64 {
	if !t.valid() {
		return 0
	}
	ts := int64(t.Timescale)
	return t.Value/ts*1_000_000_000 + t.Value%ts*1_000_000_000/ts
}

// planeBytesPerPixel is the bytes one pixel (one CbCr pair for the NV12
// chroma plane) takes in a plane, used to strip Core Video's row padding.
func planeBytesPerPixel(f camera.PixelFormat, plane int) int {
	switch f {
	case camera.NV12:
		if plane == 0 {
			return 1
		}
		return 2
	case camera.YUYV, camera.UYVY:
		return 2
	case camera.BGRA, camera.RGBA:
		return 4
	}
	return 0
}

// transportVirtual is kIOAudioDeviceTransportTypeVirtual ('virt').
const transportVirtual = 'v'<<24 | 'i'<<16 | 'r'<<8 | 't'

// deviceKind is what -deviceType says about where the camera is.
type deviceKind int

const (
	kindUnknown deviceKind = iota
	kindBuiltIn
	kindExternal // external USB/virtual, and Continuity Camera
)

// positionFor maps AVCaptureDevicePosition (0 unspecified, 1 back,
// 2 front) and the device kind. Mac built-in cameras report unspecified
// but sit in the lid facing the user.
func positionFor(avPos int, kind deviceKind) camera.Position {
	if kind == kindExternal {
		return camera.PositionExternal
	}
	switch avPos {
	case 1:
		return camera.PositionBack
	case 2:
		return camera.PositionFront
	}
	if kind == kindBuiltIn {
		return camera.PositionFront
	}
	return camera.PositionUnknown
}

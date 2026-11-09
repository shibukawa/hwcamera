// Package coreaudio is the macOS audio capture backend: device enumeration
// through the Core Audio HAL and capture through an AudioQueue input queue,
// both bound with purego so that no cgo is required.
//
// This file holds the platform-independent parts (constants, struct
// layouts, format and timestamp math) so they can be tested anywhere.
package coreaudio

import (
	"math"
	"time"

	"github.com/shibukawa/hwcamera/internal/audio"
)

// Name is the backend name.
const Name = "coreaudio"

// fourCC packs a four-character code the way Apple's headers spell them
// ('dev#' == 0x64657623).
func fourCC(s string) uint32 {
	if len(s) != 4 {
		panic("coreaudio: fourCC needs 4 bytes: " + s)
	}
	return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
}

// Core Audio HAL object IDs and property selectors (AudioHardware.h,
// AudioHardwareBase.h).
const (
	kAudioObjectSystemObject uint32 = 1
	kAudioObjectUnknown      uint32 = 0

	kAudioHardwarePropertyDevices            uint32 = 0x64657623 // 'dev#'
	kAudioHardwarePropertyDefaultInputDevice uint32 = 0x64496E20 // 'dIn '

	kAudioObjectPropertyScopeGlobal uint32 = 0x676C6F62 // 'glob'
	kAudioObjectPropertyScopeInput  uint32 = 0x696E7074 // 'inpt'
	kAudioObjectPropertyElementMain uint32 = 0

	kAudioDevicePropertyStreamConfiguration uint32 = 0x736C6179 // 'slay'
	kAudioDevicePropertyDeviceUID           uint32 = 0x75696420 // 'uid '
	kAudioObjectPropertyName                uint32 = 0x6C6E616D // 'lnam'
	kAudioDevicePropertyNominalSampleRate   uint32 = 0x6E737274 // 'nsrt'
)

// Audio format constants (CoreAudioBaseTypes.h).
const (
	kAudioFormatLinearPCM uint32 = 0x6C70636D // 'lpcm'

	kAudioFormatFlagIsFloat         uint32 = 1 << 0
	kAudioFormatFlagIsSignedInteger uint32 = 1 << 2
	kAudioFormatFlagIsPacked        uint32 = 1 << 3

	kAudioTimeStampSampleTimeValid uint32 = 1 << 0
	kAudioTimeStampHostTimeValid   uint32 = 1 << 1
)

// AudioQueue property IDs (AudioQueue.h).
const (
	kAudioQueueProperty_IsRunning     uint32 = 0x6171726E // 'aqrn'
	kAudioQueueProperty_CurrentDevice uint32 = 0x61716364 // 'aqcd'
)

// OSStatus values that map to the package's sentinel errors.
const (
	kAudioQueueErr_InvalidDevice   int32 = -66680
	kAudioQueueErr_Permissions     int32 = -66676
	kAudioQueueErr_CannotStart     int32 = -66681
	kAudioHardwareBadDeviceError   int32 = 0x21646576 // '!dev'
	kAudioHardwareIllegalOperation int32 = 0x6E6F7065 // 'nope'
	kAudioDevicePermissionsError   int32 = 0x21686F67 // '!hog'
	kAudioHardwareNotRunningError  int32 = 0x73746F70 // 'stop'
)

// kCFStringEncodingUTF8 (CFString.h).
const kCFStringEncodingUTF8 uint32 = 0x08000100

// audioObjectPropertyAddress mirrors AudioObjectPropertyAddress (12 bytes).
type audioObjectPropertyAddress struct {
	Selector uint32
	Scope    uint32
	Element  uint32
}

// audioStreamBasicDescription mirrors AudioStreamBasicDescription
// (40 bytes).
type audioStreamBasicDescription struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

// audioQueueBuffer mirrors AudioQueueBuffer on 64-bit targets (56 bytes):
// every UInt32 followed by a pointer is padded to 8.
type audioQueueBuffer struct {
	AudioDataBytesCapacity    uint32
	_                         uint32
	AudioData                 uintptr
	AudioDataByteSize         uint32
	_                         uint32
	UserData                  uintptr
	PacketDescriptionCapacity uint32
	_                         uint32
	PacketDescriptions        uintptr
	PacketDescriptionCount    uint32
	_                         uint32
}

// smpteTime mirrors SMPTETime (24 bytes).
type smpteTime struct {
	Subframes       int16
	SubframeDivisor int16
	Counter         uint32
	Type            uint32
	Flags           uint32
	Hours           int16
	Minutes         int16
	Seconds         int16
	Frames          int16
}

// audioTimeStamp mirrors AudioTimeStamp (64 bytes).
type audioTimeStamp struct {
	SampleTime    float64
	HostTime      uint64
	RateScalar    float64
	WordClockTime uint64
	SMPTETime     smpteTime
	Flags         uint32
	Reserved      uint32
}

// audioBufferHeader is the fixed part of AudioBuffer (16 bytes); an
// AudioBufferList is a UInt32 count, 4 bytes of padding, then the buffers.
type audioBuffer struct {
	NumberChannels uint32
	DataByteSize   uint32
	Data           uintptr
}

const (
	audioBufferListHeader = 8  // mNumberBuffers + padding
	audioBufferSize       = 16 // sizeof(AudioBuffer)
)

// asbdFor returns the interleaved linear PCM description of cfg.
func asbdFor(cfg audio.Config) audioStreamBasicDescription {
	size := uint32(cfg.Format.Size())
	flags := kAudioFormatFlagIsSignedInteger | kAudioFormatFlagIsPacked
	if cfg.Format == audio.F32 {
		flags = kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked
	}
	frame := size * uint32(cfg.Channels)
	return audioStreamBasicDescription{
		SampleRate:       float64(cfg.SampleRate),
		FormatID:         kAudioFormatLinearPCM,
		FormatFlags:      flags,
		BytesPerPacket:   frame,
		FramesPerPacket:  1,
		BytesPerFrame:    frame,
		ChannelsPerFrame: uint32(cfg.Channels),
		BitsPerChannel:   size * 8,
	}
}

// inputChannels sums mNumberChannels over the AudioBufferList in b, as
// returned for kAudioDevicePropertyStreamConfiguration.
func inputChannels(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	n := int(le32(b))
	total := 0
	for i := range n {
		off := audioBufferListHeader + i*audioBufferSize
		if off+4 > len(b) {
			break
		}
		total += int(le32(b[off:]))
	}
	return total
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// hostClock converts mach_absolute_time ticks with mach_timebase_info.
type hostClock struct{ numer, denom uint32 }

func (c hostClock) duration(ticks uint64) time.Duration {
	if c.numer == 0 || c.denom == 0 || c.numer == c.denom {
		return time.Duration(ticks)
	}
	// Split to avoid overflowing ticks*numer (Apple silicon is 125/3).
	q, r := ticks/uint64(c.denom), ticks%uint64(c.denom)
	return time.Duration(q*uint64(c.numer) + r*uint64(c.numer)/uint64(c.denom))
}

// clock turns the AudioTimeStamps of one stream into Buffer timestamps
// relative to the first buffer and flags gaps in the sample timeline.
type clock struct {
	host hostClock
	rate float64

	started     bool
	firstHost   uint64
	useHost     bool
	firstSample float64
	nextSample  float64 // expected mSampleTime of the next buffer
	haveNext    bool
}

// next returns the timestamp of a buffer of frames frames captured at ts
// and whether audio was lost before it.
func (c *clock) next(ts audioTimeStamp, frames int) (time.Duration, bool) {
	sampleValid := ts.Flags&kAudioTimeStampSampleTimeValid != 0
	hostValid := ts.Flags&kAudioTimeStampHostTimeValid != 0
	if !c.started {
		c.started = true
		c.useHost = hostValid
		c.firstHost = ts.HostTime
		c.firstSample = ts.SampleTime
	}
	var at time.Duration
	switch {
	case c.useHost && hostValid && ts.HostTime >= c.firstHost:
		at = c.host.duration(ts.HostTime - c.firstHost)
	case sampleValid && c.rate > 0:
		at = time.Duration((ts.SampleTime - c.firstSample) / c.rate * float64(time.Second))
	}
	gap := false
	if sampleValid {
		if c.haveNext && sampleGap(c.nextSample, ts.SampleTime, frames) {
			gap = true
		}
		c.nextSample = ts.SampleTime + float64(frames)
		c.haveNext = true
	}
	return max(at, 0), gap
}

// sampleGap reports whether a buffer starting at got, where expected was
// predicted from the previous buffer, follows a hole in the audio. Small
// jitter (a quarter of a buffer, at least two frames) is tolerated; a jump
// backwards (the device restarted its timeline) counts as a gap too.
func sampleGap(expected, got float64, frames int) bool {
	tol := math.Max(2, float64(frames)/4)
	return math.Abs(got-expected) > tol
}

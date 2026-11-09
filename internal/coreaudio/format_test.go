package coreaudio

import (
	"encoding/binary"
	"testing"
	"time"
	"unsafe"

	"github.com/shibukawa/hwcamera/internal/audio"
)

func TestFourCC(t *testing.T) {
	cases := map[string]uint32{
		"dev#": kAudioHardwarePropertyDevices,
		"dIn ": kAudioHardwarePropertyDefaultInputDevice,
		"glob": kAudioObjectPropertyScopeGlobal,
		"inpt": kAudioObjectPropertyScopeInput,
		"slay": kAudioDevicePropertyStreamConfiguration,
		"uid ": kAudioDevicePropertyDeviceUID,
		"lnam": kAudioObjectPropertyName,
		"nsrt": kAudioDevicePropertyNominalSampleRate,
		"lpcm": kAudioFormatLinearPCM,
		"aqrn": kAudioQueueProperty_IsRunning,
		"aqcd": kAudioQueueProperty_CurrentDevice,
		"!dev": uint32(kAudioHardwareBadDeviceError),
		"nope": uint32(kAudioHardwareIllegalOperation),
		"!hog": uint32(kAudioDevicePermissionsError),
		"stop": uint32(kAudioHardwareNotRunningError),
	}
	for s, want := range cases {
		if got := fourCC(s); got != want {
			t.Errorf("fourCC(%q) = %#x, want %#x", s, got, want)
		}
	}
}

func TestStructSizes(t *testing.T) {
	if s := unsafe.Sizeof(audioObjectPropertyAddress{}); s != 12 {
		t.Errorf("AudioObjectPropertyAddress = %d bytes, want 12", s)
	}
	if s := unsafe.Sizeof(audioStreamBasicDescription{}); s != 40 {
		t.Errorf("AudioStreamBasicDescription = %d bytes, want 40", s)
	}
	if s := unsafe.Sizeof(smpteTime{}); s != 24 {
		t.Errorf("SMPTETime = %d bytes, want 24", s)
	}
	if s := unsafe.Sizeof(audioTimeStamp{}); s != 64 {
		t.Errorf("AudioTimeStamp = %d bytes, want 64", s)
	}
	if unsafe.Sizeof(uintptr(0)) == 8 {
		var b audioQueueBuffer
		if s := unsafe.Sizeof(b); s != 56 {
			t.Errorf("AudioQueueBuffer = %d bytes, want 56", s)
		}
		if o := unsafe.Offsetof(b.AudioData); o != 8 {
			t.Errorf("mAudioData at %d, want 8", o)
		}
		if o := unsafe.Offsetof(b.AudioDataByteSize); o != 16 {
			t.Errorf("mAudioDataByteSize at %d, want 16", o)
		}
		if s := unsafe.Sizeof(audioBuffer{}); s != audioBufferSize {
			t.Errorf("AudioBuffer = %d bytes, want %d", s, audioBufferSize)
		}
	}
	var ts audioTimeStamp
	if o := unsafe.Offsetof(ts.Flags); o != 56 {
		t.Errorf("AudioTimeStamp.mFlags at %d, want 56", o)
	}
}

func TestASBD(t *testing.T) {
	a := asbdFor(audio.Config{SampleRate: 48000, Channels: 2, Format: audio.S16})
	want := audioStreamBasicDescription{48000, kAudioFormatLinearPCM, 0xC, 4, 1, 4, 2, 16, 0}
	if a != want {
		t.Errorf("S16 = %+v, want %+v", a, want)
	}
	a = asbdFor(audio.Config{SampleRate: 16000, Channels: 1, Format: audio.F32})
	want = audioStreamBasicDescription{16000, kAudioFormatLinearPCM, 0x9, 4, 1, 4, 1, 32, 0}
	if a != want {
		t.Errorf("F32 = %+v, want %+v", a, want)
	}
}

func TestInputChannels(t *testing.T) {
	b := make([]byte, 8+2*16)
	binary.LittleEndian.PutUint32(b, 2)
	binary.LittleEndian.PutUint32(b[8:], 2)
	binary.LittleEndian.PutUint32(b[24:], 1)
	if n := inputChannels(b); n != 3 {
		t.Errorf("got %d, want 3", n)
	}
	if n := inputChannels(b[:8]); n != 0 {
		t.Errorf("truncated: got %d, want 0", n)
	}
	if n := inputChannels(nil); n != 0 {
		t.Errorf("nil: got %d", n)
	}
}

func TestHostClock(t *testing.T) {
	if d := (hostClock{1, 1}).duration(12345); d != 12345 {
		t.Errorf("1/1: %v", d)
	}
	// Apple silicon: 24 MHz ticks, numer/denom = 125/3.
	if d := (hostClock{125, 3}).duration(24_000_000); d != time.Second {
		t.Errorf("125/3: %v", d)
	}
	// Large values must not overflow.
	ticks := uint64(24_000_000) * 3600 * 24 * 365
	if d := (hostClock{125, 3}).duration(ticks); d != 365*24*time.Hour {
		t.Errorf("year: %v", d)
	}
}

func TestClock(t *testing.T) {
	c := clock{host: hostClock{125, 3}, rate: 48000}
	both := kAudioTimeStampSampleTimeValid | kAudioTimeStampHostTimeValid
	ts := audioTimeStamp{SampleTime: 1000, HostTime: 24_000_000, Flags: both}
	if at, gap := c.next(ts, 960); at != 0 || gap {
		t.Fatalf("first: %v %v", at, gap)
	}
	ts = audioTimeStamp{SampleTime: 1960, HostTime: 24_000_000 + 480_000, Flags: both}
	if at, gap := c.next(ts, 960); at != 20*time.Millisecond || gap {
		t.Fatalf("second: %v %v", at, gap)
	}
	ts = audioTimeStamp{SampleTime: 1960 + 960*3, HostTime: 24_000_000 + 480_000*4, Flags: both}
	if at, gap := c.next(ts, 960); at != 80*time.Millisecond || !gap {
		t.Fatalf("after gap: %v %v", at, gap)
	}

	// Sample time only.
	c = clock{rate: 48000}
	ts = audioTimeStamp{SampleTime: 0, Flags: kAudioTimeStampSampleTimeValid}
	c.next(ts, 480)
	ts.SampleTime = 480 + 1 // jitter tolerated
	want := time.Duration(ts.SampleTime / 48000 * float64(time.Second))
	if at, gap := c.next(ts, 480); gap || at != want {
		t.Fatalf("sample only: %v %v", at, gap)
	}
}

func TestSampleGap(t *testing.T) {
	if sampleGap(100, 100, 480) || sampleGap(100, 200, 480) {
		t.Error("jitter flagged")
	}
	if !sampleGap(100, 300, 480) {
		t.Error("gap not flagged")
	}
	if !sampleGap(1000, 0, 480) {
		t.Error("restart not flagged")
	}
}

package wasapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/shibukawa/hwcamera/internal/audio"
)

func TestGUIDs(t *testing.T) {
	for _, tc := range []struct {
		g    GUID
		want string
	}{
		{CLSID_MMDeviceEnumerator, "{BCDE0395-E52F-467C-8E3D-C4579291692E}"},
		{IID_IMMDeviceEnumerator, "{A95664D2-9614-4F35-A746-DE8DB63617E6}"},
		{IID_IMMEndpoint, "{1BE09788-6894-4089-8586-9A2A6C265AC5}"},
		{IID_IAudioClient, "{1CB9AD4C-DBFA-4C32-B178-C2F568A703B2}"},
		{IID_IAudioCaptureClient, "{C8ADBD64-E71E-48A0-A4DE-185C395CD317}"},
		{KSDATAFORMAT_SUBTYPE_PCM, "{00000001-0000-0010-8000-00AA00389B71}"},
		{KSDATAFORMAT_SUBTYPE_IEEE_FLOAT, "{00000003-0000-0010-8000-00AA00389B71}"},
		{PKEY_Device_FriendlyName.FmtID, "{A45C254E-DF1C-4EFD-8020-67D146A850E0}"},
	} {
		if got := tc.g.String(); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
	}
	if PKEY_Device_FriendlyName.PID != 14 {
		t.Errorf("FriendlyName pid = %d", PKEY_Device_FriendlyName.PID)
	}
	b := KSDATAFORMAT_SUBTYPE_PCM.bytes()
	want := []byte{1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71}
	if !bytes.Equal(b[:], want) {
		t.Errorf("PCM GUID bytes = % x", b)
	}
}

func TestWaveFormatExtensible(t *testing.T) {
	le := binary.LittleEndian
	for _, tc := range []struct {
		cfg         audio.Config
		bits, block int
		mask        uint32
		sub         GUID
	}{
		{audio.Config{SampleRate: 48000, Channels: 1, Format: audio.S16}, 16, 2, 0x4, KSDATAFORMAT_SUBTYPE_PCM},
		{audio.Config{SampleRate: 44100, Channels: 2, Format: audio.F32}, 32, 8, 0x3, KSDATAFORMAT_SUBTYPE_IEEE_FLOAT},
		{audio.Config{SampleRate: 16000, Channels: 4, Format: audio.S16}, 16, 8, 0, KSDATAFORMAT_SUBTYPE_PCM},
	} {
		b := waveFormatExtensible(tc.cfg)
		if len(b) != 40 {
			t.Fatalf("size %d", len(b))
		}
		check := func(name string, got, want uint32) {
			if got != want {
				t.Errorf("%v %s = %d, want %d", tc.cfg, name, got, want)
			}
		}
		check("wFormatTag", uint32(le.Uint16(b[0:])), 0xFFFE)
		check("nChannels", uint32(le.Uint16(b[2:])), uint32(tc.cfg.Channels))
		check("nSamplesPerSec", le.Uint32(b[4:]), uint32(tc.cfg.SampleRate))
		check("nAvgBytesPerSec", le.Uint32(b[8:]), uint32(tc.cfg.SampleRate*tc.block))
		check("nBlockAlign", uint32(le.Uint16(b[12:])), uint32(tc.block))
		check("wBitsPerSample", uint32(le.Uint16(b[14:])), uint32(tc.bits))
		check("cbSize", uint32(le.Uint16(b[16:])), 22)
		check("wValidBitsPerSample", uint32(le.Uint16(b[18:])), uint32(tc.bits))
		check("dwChannelMask", le.Uint32(b[20:]), tc.mask)
		sub := tc.sub.bytes()
		if !bytes.Equal(b[24:], sub[:]) {
			t.Errorf("%v SubFormat = % x", tc.cfg, b[24:])
		}
		rate, ch, ok := parseWaveFormat(b[:])
		if !ok || rate != tc.cfg.SampleRate || ch != tc.cfg.Channels {
			t.Errorf("parseWaveFormat = %d %d %v", rate, ch, ok)
		}
	}
}

func TestHRError(t *testing.T) {
	for _, tc := range []struct {
		hr   HRESULT
		want error
	}{
		{E_ACCESSDENIED, audio.ErrPermission},
		{AUDCLNT_E_DEVICE_IN_USE, audio.ErrBusy},
		{AUDCLNT_E_DEVICE_INVALIDATED, audio.ErrClosed},
		{AUDCLNT_E_UNSUPPORTED_FORMAT, audio.ErrUnsupported},
	} {
		err := hrError("op", tc.hr)
		if !errors.Is(err, tc.want) {
			t.Errorf("0x%08X: %v is not %v", uint32(tc.hr), err, tc.want)
		}
	}
	err := hrError("IAudioClient.Initialize", E_FAIL)
	var be *audio.BackendError
	if !errors.As(err, &be) || be.Backend != "wasapi" || be.Op != "IAudioClient.Initialize" || be.Code != 0x80004005 {
		t.Errorf("generic error = %#v", err)
	}
	for _, s := range []error{audio.ErrPermission, audio.ErrBusy, audio.ErrClosed, audio.ErrNotFound} {
		if errors.Is(err, s) {
			t.Errorf("E_FAIL maps to %v", s)
		}
	}
	if err := lookupError("GetDevice", E_NOTFOUND, audio.ErrNotFound); !errors.Is(err, audio.ErrNotFound) {
		t.Errorf("E_NOTFOUND: %v", err)
	}
	if err := lookupError("GetDefaultAudioEndpoint", E_NOTFOUND, audio.ErrNoDevice); !errors.Is(err, audio.ErrNoDevice) {
		t.Errorf("default E_NOTFOUND: %v", err)
	}
	if err := lookupError("GetDevice", E_ACCESSDENIED, audio.ErrNotFound); !errors.Is(err, audio.ErrPermission) {
		t.Errorf("GetDevice E_ACCESSDENIED: %v", err)
	}
	if bufferDuration(audio.Config{Period: 20 * time.Millisecond}) != 1_000_000 {
		t.Error("bufferDuration minimum")
	}
	if bufferDuration(audio.Config{Period: 50 * time.Millisecond}) != 2_000_000 {
		t.Error("bufferDuration 4 periods")
	}
}

func TestAccumulator(t *testing.T) {
	cfg := audio.Config{SampleRate: 1000, Channels: 2, Format: audio.S16, Period: 10 * time.Millisecond}
	// 10 frames per period, 4 bytes per frame.
	a := newAccumulator(cfg)
	var out []*audio.Buffer
	emit := func(b *audio.Buffer) { out = append(out, b) }

	pkt := func(frames int, v byte) []byte { return bytes.Repeat([]byte{v}, frames*4) }
	const ms = 10_000 // 100 ns units

	a.add(pkt(7, 1), 7, 5000*ms, true, emit) // first packet: disc ignored
	if len(out) != 0 {
		t.Fatalf("emitted early: %d", len(out))
	}
	a.add(pkt(7, 2), 7, 5007*ms, false, emit)
	a.add(nil, 7, 5014*ms, true, emit) // silent, after a gap
	a.add(pkt(9, 3), 9, 5021*ms, false, emit)
	// 30 frames total → 3 buffers.
	if len(out) != 3 {
		t.Fatalf("got %d buffers", len(out))
	}
	wantTS := []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond}
	wantDisc := []bool{false, true, false}
	for i, b := range out {
		if b.Frames != 10 || len(b.Data) != 40 || b.Channels != 2 || b.SampleRate != 1000 || b.Format != audio.S16 {
			t.Errorf("buf %d: frames %d len %d", i, b.Frames, len(b.Data))
		}
		if b.Timestamp != wantTS[i] {
			t.Errorf("buf %d: ts %v want %v", i, b.Timestamp, wantTS[i])
		}
		if b.Discontinuity != wantDisc[i] {
			t.Errorf("buf %d: disc %v", i, b.Discontinuity)
		}
	}
	want0 := append(pkt(7, 1), pkt(3, 2)...)
	if !bytes.Equal(out[0].Data, want0) {
		t.Errorf("buf 0 data % x", out[0].Data)
	}
	want1 := append(pkt(4, 2), make([]byte, 6*4)...)
	if !bytes.Equal(out[1].Data, want1) {
		t.Errorf("buf 1 data % x", out[1].Data)
	}
	want2 := append(make([]byte, 1*4), pkt(9, 3)...)
	if !bytes.Equal(out[2].Data, want2) {
		t.Errorf("buf 2 data % x", out[2].Data)
	}
	// Buffers must not alias each other or the input.
	src := pkt(25, 9)
	a.add(src, 25, 5030*ms, false, emit)
	src[0] = 0
	if len(out) != 5 || out[3].Data[0] != 9 {
		t.Errorf("aliasing or count: %d", len(out))
	}
	if out[4].Timestamp != 40*time.Millisecond {
		t.Errorf("buf 4 ts %v", out[4].Timestamp)
	}
}

func TestTimestamper(t *testing.T) {
	ts := timestamper{rate: 48000}
	if got := ts.time(480, 0, 0, 1_000_000); got != 1_000_000 {
		t.Errorf("qpc %d", got)
	}
	if got := ts.time(480, AUDCLNT_BUFFERFLAGS_TIMESTAMP_ERROR, 0, 0); got != 1_100_000 {
		t.Errorf("extrapolated %d", got)
	}
	dp := timestamper{rate: 48000}
	if got := dp.time(480, 0, 48000, 0); got != 10_000_000 {
		t.Errorf("devpos %d", got)
	}
}

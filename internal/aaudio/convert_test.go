package aaudio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	iaudio "github.com/shibukawa/hwcamera/internal/audio"
)

func s16(v ...int16) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint16(b, uint16(x))
	}
	return b
}

func f32s(b []byte) []float32 {
	var out []float32
	for i := 0; i+4 <= len(b); i += 4 {
		out = append(out, math.Float32frombits(binary.LittleEndian.Uint32(b[i:])))
	}
	return out
}

func cfg(rate, ch int, f iaudio.SampleFormat) iaudio.Config {
	return iaudio.Config{SampleRate: rate, Channels: ch, Format: f}
}

func TestIdentity(t *testing.T) {
	c, _ := newConverter(48000, 2, formatI16, cfg(48000, 2, iaudio.S16))
	in := s16(1, 2, 3, 4)
	if !c.identity() || !bytes.Equal(c.convert(nil, in, 2), in) {
		t.Fatal("identity")
	}
}

func TestFormatAndChannels(t *testing.T) {
	// stereo S16 -> mono F32: average
	c, _ := newConverter(48000, 2, formatI16, cfg(48000, 1, iaudio.F32))
	got := f32s(c.convert(nil, s16(16384, 0, -32768, -32768), 2))
	if len(got) != 2 || got[0] != 0.25 || got[1] != -1 {
		t.Fatalf("stereo->mono %v", got)
	}
	// mono F32 -> stereo S16: duplicate, clamp
	var in []byte
	for _, v := range []float32{0.5, 1.5} {
		in = binary.LittleEndian.AppendUint32(in, math.Float32bits(v))
	}
	c, _ = newConverter(48000, 1, formatFloat, cfg(48000, 2, iaudio.S16))
	if out := c.convert(nil, in, 2); !bytes.Equal(out, s16(16384, 16384, 32767, 32767)) {
		t.Fatalf("mono->stereo %v", out)
	}
	// I24 packed and I32
	if v := decode([]byte{0, 0, 0x40}, formatI24Packed); v != 0.5 {
		t.Fatalf("i24 %v", v)
	}
	if v := decode([]byte{0, 0, 0, 0x80}, formatI32); v != -1 {
		t.Fatalf("i32 %v", v)
	}
	if v := decode([]byte{0xff, 0xff, 0xff}, formatI24Packed); v != -1.0/(1<<23) {
		t.Fatalf("i24 neg %v", v)
	}
}

func TestResample(t *testing.T) {
	for _, rates := range [][2]int{{48000, 16000}, {44100, 48000}, {16000, 48000}} {
		in, out := rates[0], rates[1]
		const secs = 1
		n := in * secs
		src := make([]byte, 0, n*2)
		for i := 0; i < n; i++ {
			src = binary.LittleEndian.AppendUint16(src, uint16(int16(10000*math.Sin(2*math.Pi*440*float64(i)/float64(in)))))
		}
		whole, _ := newConverter(in, 1, formatI16, cfg(out, 1, iaudio.F32))
		a := whole.convert(nil, src, n)
		chunked, _ := newConverter(in, 1, formatI16, cfg(out, 1, iaudio.F32))
		var b []byte
		for off := 0; off < n; {
			k := min(337, n-off)
			b = chunked.convert(b, src[off*2:], k)
			off += k
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%v: chunked output differs (%d vs %d bytes)", rates, len(a), len(b))
		}
		got := f32s(a)
		// Up to one input frame of output waits for the next input.
		if d := len(got) - out*secs; d < -(out/in+2) || d > 0 {
			t.Fatalf("%v: %d frames out", rates, len(got))
		}
		// The output is the same sine at the new rate.
		var maxErr float64
		for i, v := range got {
			want := 10000.0 / 32768 * math.Sin(2*math.Pi*440*float64(i)/float64(out))
			maxErr = max(maxErr, math.Abs(float64(v)-want))
		}
		if maxErr > 0.01 {
			t.Fatalf("%v: max error %v", rates, maxErr)
		}
	}
}

func TestAccumulator(t *testing.T) {
	a := accumulator{period: 4}
	a.buf = append(a.buf, 1, 2, 3, 4, 5, 6)
	p, ok := a.next()
	if !ok || !bytes.Equal(p, []byte{1, 2, 3, 4}) {
		t.Fatal(p)
	}
	if _, ok := a.next(); ok {
		t.Fatal("partial period")
	}
	if !bytes.Equal(a.buf, []byte{5, 6}) {
		t.Fatal(a.buf)
	}
}

func TestErrors(t *testing.T) {
	if !errors.Is(resultErr("read", errDisconnected, ""), iaudio.ErrClosed) {
		t.Fatal("disconnected")
	}
	if _, err := newConverter(48000, 1, 99, cfg(48000, 1, iaudio.S16)); !errors.Is(err, iaudio.ErrUnsupported) {
		t.Fatal("format")
	}
}

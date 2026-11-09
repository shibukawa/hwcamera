package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/shibukawa/hwcamera/audio"
)

func mics(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mics", flag.ExitOnError)
	monitors := fs.Bool("monitors", false, "also list the monitors of the outputs (Linux)")
	fs.Parse(args)
	ds, err := audio.Devices(ctx)
	if err != nil {
		return err
	}
	shown := 0
	for _, d := range ds {
		if d.Monitor && !*monitors {
			continue
		}
		mark := " "
		if d.Default {
			mark = "*"
		}
		kind := ""
		if d.Monitor {
			kind = ", monitor"
		}
		fmt.Printf("%s %s  \"%s\"  [%s%s, %d Hz, %d ch]\n", mark, d.ID, d.Name, d.Backend, kind, d.SampleRate, d.Channels)
		shown++
	}
	if shown == 0 {
		fmt.Println("no audio inputs (backends:", strings.Join(audio.Backends(), ", ")+")")
	}
	return nil
}

// rec records to a WAV file and shows a level meter.
func rec(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rec", flag.ExitOnError)
	device := fs.String("device", "", "input ID (see mics); empty for the default input")
	out := fs.String("o", "rec.wav", "output WAV file")
	seconds := fs.Float64("seconds", 5, "length (0 = until Ctrl-C)")
	rate := fs.Int("rate", 48000, "sample rate")
	channels := fs.Int("channels", 1, "channels")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	mic, err := audio.Open(ctx, *device, audio.WithSampleRate(*rate), audio.WithChannels(*channels))
	if err != nil {
		return err
	}
	defer mic.Close()
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	writeWAVHeader(f, mic.SampleRate(), mic.Channels(), 0)
	fmt.Printf("recording \"%s\" (%s) at %d Hz, %d ch -> %s\n", mic.Info().Name, mic.Info().Backend, mic.SampleRate(), mic.Channels(), *out)

	limit := time.Duration(*seconds * float64(time.Second))
	var frames, gaps int
	var total time.Duration
	for limit == 0 || total < limit {
		b, err := mic.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		if b.Discontinuity {
			gaps++
		}
		if _, err := f.Write(b.Data); err != nil {
			return err
		}
		frames += b.Frames
		total = b.Timestamp + b.Duration()
		peak, rms := b.Level()
		n := int(rms * 200)
		fmt.Printf("\r%6.2fs  %-50s peak %.2f", total.Seconds(), strings.Repeat("#", min(n, 50)), peak)
	}
	fmt.Println()
	f.Seek(0, 0)
	writeWAVHeader(f, mic.SampleRate(), mic.Channels(), frames)
	fmt.Printf("%s: %d frames (%.2fs), %d discontinuities\n", *out, frames, float64(frames)/float64(mic.SampleRate()), gaps)
	return nil
}

func writeWAVHeader(f *os.File, rate, channels, frames int) {
	data := uint32(frames * channels * 2)
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+data)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], data)
	f.Write(h)
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/audio"
	"github.com/shibukawa/hwcamera/cmd/internal/session"
)

// rec records the camera and the microphone into one MP4 (hardware H.264
// / HEVC and AAC), the camera alone into MP4, or the microphone alone into
// WAV, until -duration or Ctrl-C.
func rec(ctx context.Context, args []string) error {
	s := session.Defaults()
	fs := flag.NewFlagSet("rec", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: hwcamera-cli rec [flags]\n\nRecords the camera and the microphone into <o>.mp4 (hardware H.264/HEVC + AAC)\nuntil -duration or Ctrl-C. -media audio writes <o>.wav instead.\n\nflags:")
		fs.PrintDefaults()
	}
	session.RegisterRecFlags(fs, &s)
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	// The first failure stops the other track too. -duration starts
	// once the devices are open (opening a camera can take seconds).
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	openCtx := ctx
	var startRec func()
	ctx, startRec = deferredTimeout(ctx, s.Duration)

	var (
		wg    sync.WaitGroup
		level atomic.Uint64 // rms * 1e6
		cam   *hwcamera.Camera
		mic   *audio.Mic
		lines []string
		err   error
	)
	if s.Media&session.Video != 0 {
		if cam, err = session.OpenCamera(openCtx, s, hwcamera.NV12); err != nil {
			return err
		}
		defer cam.Close()
		lines = append(lines, fmt.Sprintf("video: \"%s\" (%s, %s, %s)", cam.Info().Name, cam.Info().Backend, cam.Mode(), s.Codec))
	}
	if s.Media&session.Audio != 0 {
		if mic, err = session.OpenMic(openCtx, s); err != nil {
			return err
		}
		defer mic.Close()
		kind := "WAV"
		if s.Media == session.Both {
			kind = "AAC"
		}
		lines = append(lines, fmt.Sprintf("audio: \"%s\" (%s, %d Hz, %d ch, %s)", mic.Info().Name, mic.Info().Backend, mic.SampleRate(), mic.Channels(), kind))
	}
	r, err := session.StartRecording(openCtx, s, cam, mic)
	if err != nil {
		return err
	}
	lines = append(lines, "-> "+strings.Join(r.Files(), ", "))
	if cam != nil {
		wg.Go(func() {
			for {
				f, err := cam.Read(ctx)
				if err != nil {
					if ctx.Err() == nil && !errors.Is(err, hwcamera.ErrClosed) {
						cancel(fmt.Errorf("camera: %w", err))
					}
					return
				}
				err = r.WriteVideo(ctx, f)
				f.Release()
				if err != nil {
					if ctx.Err() == nil {
						cancel(fmt.Errorf("video: %w", err))
					}
					return
				}
			}
		})
	}
	if mic != nil {
		wg.Go(func() {
			for {
				b, err := mic.Read(ctx)
				if err != nil {
					if ctx.Err() == nil {
						cancel(fmt.Errorf("microphone: %w", err))
					}
					return
				}
				if err := r.WriteAudio(ctx, b); err != nil {
					if ctx.Err() == nil {
						cancel(fmt.Errorf("audio: %w", err))
					}
					return
				}
				_, rms := b.Level()
				level.Store(uint64(rms * 1e6))
			}
		})
	}
	startRec()
	for _, l := range lines {
		fmt.Println(l)
	}
	if s.Duration > 0 {
		fmt.Printf("recording for %s (Ctrl-C to stop early)\n", s.Duration)
	} else {
		fmt.Println("recording; Ctrl-C to stop")
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	start := time.Now()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	show := func() {
		var b strings.Builder
		fmt.Fprintf(&b, "\r%6.1fs", time.Since(start).Seconds())
		if s.Media&session.Video != 0 {
			fmt.Fprintf(&b, "  %5d frames", r.Frames())
		}
		if s.Media&session.Audio != 0 {
			n := min(int(float64(level.Load())/1e6*200), 30)
			fmt.Fprintf(&b, "  audio %6.1fs %-30s", r.AudioDuration().Seconds(), strings.Repeat("#", n))
		}
		fmt.Print(b.String())
	}
loop:
	for {
		select {
		case <-done:
			break loop
		case <-tick.C:
			show()
		}
	}
	show()
	fmt.Println()

	err = context.Cause(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = nil
	}
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	for _, f := range s.Files() {
		fmt.Println("wrote", f)
	}
	return nil
}

// deferredTimeout returns a context that ends d after start is called
// (never, for d == 0), or when parent ends.
func deferredTimeout(parent context.Context, d time.Duration) (ctx context.Context, start func()) {
	ctx, cancel := context.WithCancelCause(parent)
	return ctx, func() {
		if d > 0 {
			time.AfterFunc(d, func() { cancel(context.DeadlineExceeded) })
		}
	}
}

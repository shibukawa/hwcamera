package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/audio"
	"github.com/shibukawa/hwcamera/cmd/internal/session"
)

// capture reads the camera on a goroutine. Each frame goes to the video
// recorder when one is set, then becomes the newest frame for the window,
// so a slow window never drops frames from a recording.
type capture struct {
	cam    *hwcamera.Camera
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	latest *hwcamera.Frame // newest frame not yet shown
	err    error           // camera error
	rec    *session.Recording
	recE   error // first error of writing to rec
	frames int
	start  time.Time
}

// startCapture opens the camera of s in RGBA: the window shows the frames
// as they are, and the encoders take RGBA too.
func startCapture(s session.Settings) (*capture, error) {
	ctx, cancel := context.WithCancel(context.Background())
	octx, ocancel := context.WithTimeout(ctx, 15*time.Second)
	defer ocancel()
	cam, err := session.OpenCamera(octx, s, hwcamera.RGBA)
	if err != nil {
		cancel()
		return nil, err
	}
	c := &capture{cam: cam, cancel: cancel, done: make(chan struct{}), start: time.Now()}
	go c.run(ctx)
	return c, nil
}

func (c *capture) run(ctx context.Context) {
	defer close(c.done)
	for {
		f, err := c.cam.Read(ctx)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, hwcamera.ErrClosed) {
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
			}
			return
		}
		c.mu.Lock()
		if c.rec != nil && c.recE == nil {
			c.recE = c.rec.WriteVideo(ctx, f)
		}
		if c.latest != nil {
			c.latest.Release()
		}
		c.latest = f
		c.frames++
		c.mu.Unlock()
	}
}

// take returns the newest frame, or nil when none arrived since the last call.
func (c *capture) take() *hwcamera.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.latest
	c.latest = nil
	return f
}

// setRecorder sets the recorder frames go to and returns the previous
// one with its error. No frame is written to the previous one afterwards.
func (c *capture) setRecorder(r *session.Recording) (*session.Recording, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, err := c.rec, c.recE
	c.rec, c.recE = r, nil
	return old, err
}

func (c *capture) recErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recE
}

func (c *capture) stats() (fps float64, recorded int64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rec != nil {
		recorded = c.rec.Frames()
	}
	return float64(c.frames) / time.Since(c.start).Seconds(), recorded, c.err
}

func (c *capture) stop() {
	c.cancel()
	c.cam.Close()
	<-c.done
	if f := c.take(); f != nil {
		f.Release()
	}
}

// micCapture reads the microphone on a goroutine for the level meter and,
// while recording, into a WAV file.
type micCapture struct {
	m      *audio.Mic
	cancel context.CancelFunc
	done   chan struct{}
	level  atomic.Uint64 // rms * 1e6

	mu  sync.Mutex
	w   *session.Recording
	wE  error
	err error
}

func startMic(s session.Settings) (*micCapture, error) {
	ctx, cancel := context.WithCancel(context.Background())
	octx, ocancel := context.WithTimeout(ctx, 15*time.Second)
	defer ocancel()
	m, err := session.OpenMic(octx, s)
	if err != nil {
		cancel()
		return nil, err
	}
	c := &micCapture{m: m, cancel: cancel, done: make(chan struct{})}
	go c.run(ctx)
	return c, nil
}

func (c *micCapture) run(ctx context.Context) {
	defer close(c.done)
	for {
		b, err := c.m.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
			}
			return
		}
		_, rms := b.Level()
		c.level.Store(uint64(rms * 1e6))
		c.mu.Lock()
		if c.w != nil && c.wE == nil {
			c.wE = c.w.WriteAudio(ctx, b)
		}
		c.mu.Unlock()
	}
}

// Level is the RMS of the last buffer, 0 to 1.
func (c *micCapture) Level() float64 { return float64(c.level.Load()) / 1e6 }

func (c *micCapture) setWriter(w *session.Recording) (*session.Recording, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, err := c.w, c.wE
	c.w, c.wE = w, nil
	return old, err
}

func (c *micCapture) writeErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wE
}

func (c *micCapture) readErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *micCapture) stop() {
	c.cancel()
	c.m.Close()
	<-c.done
}

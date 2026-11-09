package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRingDropsOldestAndFlags(t *testing.T) {
	r := NewRing(2)
	for i := range 3 {
		r.Push(&Buffer{Frames: i})
	}
	ctx := context.Background()
	b, _ := r.Pop(ctx)
	if b.Frames != 1 || !b.Discontinuity {
		t.Fatalf("first = %+v, want frames 1 with discontinuity", b)
	}
	b, _ = r.Pop(ctx)
	if b.Frames != 2 || b.Discontinuity {
		t.Fatalf("second = %+v", b)
	}
	r.Fail(ErrClosed)
	if _, err := r.Pop(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("after Fail: %v", err)
	}
}

func TestRingPopHonoursContext(t *testing.T) {
	r := NewRing(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := r.Pop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigDefaults(t *testing.T) {
	var c Config
	c.Normalize()
	if c.SampleRate != 48000 || c.Channels != 1 || c.Format != S16 || c.PeriodFrames() != 960 {
		t.Fatalf("%+v %d", c, c.PeriodFrames())
	}
}

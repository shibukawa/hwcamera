package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCaptureFromDefaultInput runs against the machine's real sound
// system and skips where there is none (CI containers).
func TestCaptureFromDefaultInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ds, err := Devices(ctx)
	if err != nil || len(ds) == 0 {
		t.Skipf("no audio inputs: %v", err)
	}
	mic, err := Open(ctx, "", WithSampleRate(16000), WithChannels(2), WithFormat(F32), WithPeriod(10*time.Millisecond))
	if errors.Is(err, ErrPermission) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer mic.Close()
	var last time.Duration = -1
	for i := range 10 {
		b, err := mic.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if b.Format != F32 || b.SampleRate != 16000 || b.Channels != 2 || b.Frames != 160 || len(b.F32()) != 320 {
			t.Fatalf("buffer %d: %v %d Hz %d ch %d frames %d samples", i, b.Format, b.SampleRate, b.Channels, b.Frames, len(b.F32()))
		}
		if b.Timestamp <= last {
			t.Fatalf("timestamp %v after %v", b.Timestamp, last)
		}
		last = b.Timestamp
	}
	done := make(chan error, 1)
	go func() { _, err := mic.Read(ctx); _, err = mic.Read(ctx); done <- err }()
	mic.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not return after Close")
	}
}

func TestOpenUnknown(t *testing.T) {
	ctx := context.Background()
	if _, err := Devices(ctx); err != nil {
		t.Skip(err)
	}
	if _, err := Open(ctx, "no-such-input"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

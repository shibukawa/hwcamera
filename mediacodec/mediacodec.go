// Package mediacodec connects hwcamera to the hardware encoders of
// github.com/shibukawa/hwmediacodec.
//
// A Source is a hwmediacodec.FrameReader over a camera, so the pipeline
// helpers of hwmediacodec work unchanged:
//
//	cam, _ := hwcamera.Open(ctx, "", hwcamera.WithSize(1280, 720))
//	enc, _ := mediacodec.NewEncoder(ctx, cam, hwmediacodec.H264, hwmediacodec.WithBitrate(4_000_000))
//	file, _ := mp4.CreateVideoFile("out.mp4", hwmediacodec.H264, mediacodec.TimeScale)
//	w := hwmediacodec.NewEncodeWriter(enc, file)
//	hwmediacodec.CopyFrames(ctx, w, mediacodec.NewSource(cam)) // until ctx is cancelled
//	w.Flush(context.Background()); file.Close()
//
// Record wraps exactly that loop and treats the cancellation of ctx as the
// normal end of the recording.
package mediacodec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwmediacodec"
)

// TimeScale is the PTS unit of the frames a Source returns (90 kHz, the
// default of hwmediacodec encoders and of MPEG transport).
const TimeScale = 90000

// Source reads camera frames as hwmediacodec frames.
//
// hwmediacodec frames cannot carry a release callback, so the camera frame
// behind the last returned frame is released on the next ReadFrame (or on
// Close). That matches how encoders use frames: Encoder.Send copies the
// pixels before it returns. Keep a frame past the next ReadFrame only after
// copying it.
type Source struct {
	cam  *hwcamera.Camera
	mu   sync.Mutex
	last *hwcamera.Frame
	// MaxFrames stops the source with io.EOF after that many frames (0: no limit).
	MaxFrames int
	// Duration stops the source with io.EOF once a frame's timestamp
	// reaches it (0: no limit).
	Duration time.Duration
	n        int
}

// NewSource returns a frame reader over cam. Open the camera with the
// default NV12 format (or RGBA/BGRA); the encoders take nothing else.
func NewSource(cam *hwcamera.Camera) *Source { return &Source{cam: cam} }

// ReadFrame implements hwmediacodec.FrameReader. A cancelled ctx ends the
// stream with io.EOF so CopyFrames returns cleanly; Record relies on that.
func (s *Source) ReadFrame(ctx context.Context) (*hwmediacodec.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.Release()
		s.last = nil
	}
	if s.MaxFrames > 0 && s.n >= s.MaxFrames {
		return nil, io.EOF
	}
	f, err := s.cam.Read(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, hwcamera.ErrClosed) {
			return nil, io.EOF
		}
		return nil, err
	}
	if s.Duration > 0 && f.Timestamp >= s.Duration {
		f.Release()
		return nil, io.EOF
	}
	hf, err := Frame(f)
	if err != nil {
		f.Release()
		return nil, err
	}
	s.last = f
	s.n++
	return hf, nil
}

// Close releases the frame still held.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.Release()
		s.last = nil
	}
	return nil
}

// Frame wraps a camera frame as a hwmediacodec frame sharing its memory,
// with the PTS in TimeScale units. The camera frame must stay unreleased
// while the result is in use.
func Frame(f *hwcamera.Frame) (*hwmediacodec.Frame, error) {
	var pf hwmediacodec.PixelFormat
	switch f.Format {
	case hwcamera.NV12:
		pf = hwmediacodec.NV12
	case hwcamera.RGBA:
		pf = hwmediacodec.RGBA
	case hwcamera.BGRA:
		pf = hwmediacodec.BGRA
	default:
		return nil, fmt.Errorf("mediacodec: encoders take NV12, RGBA or BGRA, camera gives %s (open it with hwcamera.WithFormat(hwcamera.NV12))", f.Format)
	}
	return &hwmediacodec.Frame{
		Width:   f.Width,
		Height:  f.Height,
		Format:  pf,
		Planes:  f.Planes,
		Strides: f.Strides,
		PTS:     int64(f.Timestamp) * TimeScale / int64(time.Second),
	}, nil
}

// NewEncoder opens a hardware encoder for the camera's size, input format
// and frame rate. Options are passed on and may override those.
func NewEncoder(ctx context.Context, cam *hwcamera.Camera, c hwmediacodec.Codec, opts ...hwmediacodec.EncoderOption) (hwmediacodec.Encoder, error) {
	w, h := cam.Size()
	var base []hwmediacodec.EncoderOption
	switch cam.Format() {
	case hwcamera.NV12:
		base = append(base, hwmediacodec.WithInputFormat(hwmediacodec.NV12))
	case hwcamera.RGBA:
		base = append(base, hwmediacodec.WithInputFormat(hwmediacodec.RGBA))
	case hwcamera.BGRA:
		base = append(base, hwmediacodec.WithInputFormat(hwmediacodec.BGRA))
	default:
		return nil, fmt.Errorf("mediacodec: camera delivers %s; open it with NV12, RGBA or BGRA", cam.Format())
	}
	if fr := cam.Mode().MaxFrameRate(); fr > 0 {
		base = append(base, hwmediacodec.WithFrameRate(fr))
	}
	base = append(base, hwmediacodec.WithTimeScale(TimeScale))
	return hwmediacodec.NewEncoder(ctx, c, w, h, append(base, opts...)...)
}

// Record encodes camera frames into dst until ctx is cancelled or its
// deadline passes (the normal ways to stop) or the camera fails, then flushes the encoder. It returns the
// number of frames encoded. Closing dst is the caller's job.
func Record(ctx context.Context, cam *hwcamera.Camera, enc hwmediacodec.Encoder, dst hwmediacodec.PacketWriter) (int64, error) {
	src := NewSource(cam)
	defer src.Close()
	w := hwmediacodec.NewEncodeWriter(enc, dst)
	n, err := hwmediacodec.CopyFrames(ctx, w, src)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return n, err
	}
	// ctx is done by now; the flush needs a live one.
	fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return n, w.Flush(fctx)
}

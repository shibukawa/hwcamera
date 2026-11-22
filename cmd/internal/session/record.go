package session

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/audio"
	"github.com/shibukawa/hwcamera/mediacodec"
	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// OpenCamera opens the camera of s, delivering frames in format (NV12 for
// recording only, RGBA when the frames are also shown).
func OpenCamera(ctx context.Context, s Settings, format hwcamera.PixelFormat) (*hwcamera.Camera, error) {
	return hwcamera.Open(ctx, s.Camera,
		hwcamera.WithSize(s.Size.W, s.Size.H),
		hwcamera.WithFrameRate(s.FPS),
		hwcamera.WithFormat(format))
}

// OpenMic opens the microphone of s with 16-bit samples, the format of
// the WAV files.
func OpenMic(ctx context.Context, s Settings) (*audio.Mic, error) {
	return audio.Open(ctx, s.Mic,
		audio.WithSampleRate(s.Rate),
		audio.WithChannels(s.Channels),
		audio.WithFormat(audio.S16))
}

// ParseCodec reads h264 or hevc.
func ParseCodec(s string) (hwmediacodec.Codec, error) {
	switch strings.ToLower(s) {
	case "h264", "avc", "":
		return hwmediacodec.H264, nil
	case "hevc", "h265":
		return hwmediacodec.HEVC, nil
	}
	return 0, fmt.Errorf("unknown codec %q (h264 or hevc)", s)
}

// Recording writes the tracks of s: video and audio into one MP4 (the
// hardware H.264 / HEVC encoder and AAC), video alone into an MP4, or
// audio alone into a WAV file. Both tracks count time from the start of
// the recording, so a recording can begin while the devices are already
// running and the two tracks stay in step.
//
// WriteVideo and WriteAudio may be called from different goroutines;
// Close must come after the last of them.
type Recording struct {
	Settings Settings
	start    time.Time

	file   interface{ Close() error } // *mp4.AVFile or *mp4.VideoFile
	video  *videoTrack
	aac    *aacTrack
	wav    *WAVWriter
	closed bool
}

// StartRecording opens the encoders and creates the file of s. cam is
// needed when s records video, mic when it records audio.
func StartRecording(ctx context.Context, s Settings, cam *hwcamera.Camera, mic *audio.Mic) (*Recording, error) {
	r := &Recording{Settings: s}
	ok := false
	defer func() {
		if !ok {
			r.abort()
		}
	}()
	switch s.Media {
	case Audio:
		w, err := CreateWAV(s.AudioPath(), mic.SampleRate(), mic.Channels())
		if err != nil {
			return nil, err
		}
		r.wav = w
	case Video, Both:
		codec, err := ParseCodec(s.Codec)
		if err != nil {
			return nil, err
		}
		enc, err := mediacodec.NewEncoder(ctx, cam, codec, hwmediacodec.WithBitrate(s.Bitrate), hwmediacodec.WithLowLatency())
		if err != nil {
			return nil, fmt.Errorf("video encoder: %w", err)
		}
		r.video = &videoTrack{enc: enc, origin: -1}
		var dst hwmediacodec.PacketWriteCloser
		if s.Media == Both {
			aenc, err := hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, mic.SampleRate(), mic.Channels())
			if err != nil {
				return nil, fmt.Errorf("audio encoder: %w", err)
			}
			r.aac = &aacTrack{enc: aenc, rate: mic.SampleRate(), ch: mic.Channels()}
			f, err := mp4.CreateAVFile(s.VideoPath(), codec, mediacodec.TimeScale, aenc.Format(), uint32(mic.SampleRate()))
			if err != nil {
				return nil, err
			}
			r.file, dst, r.aac.dst = f, f.Video(), f.Audio()
		} else {
			f, err := mp4.CreateVideoFile(s.VideoPath(), codec, mediacodec.TimeScale)
			if err != nil {
				return nil, err
			}
			r.file, dst = f, f
		}
		r.video.dst = dst
		r.video.w = hwmediacodec.NewEncodeWriter(enc, dst)
	default:
		return nil, fmt.Errorf("nothing to record")
	}
	r.start = time.Now()
	r.Settings = s
	ok = true
	return r, nil
}

// since is the time from the start of the recording to now: where the
// first sample of a track goes.
func (r *Recording) since() time.Duration { return max(0, time.Since(r.start)) }

// WriteVideo encodes f (the pixels are copied; releasing f stays the
// caller's job). It does nothing when the recording has no video.
func (r *Recording) WriteVideo(ctx context.Context, f *hwcamera.Frame) error {
	t := r.video
	if t == nil {
		return nil
	}
	hf, err := mediacodec.Frame(f)
	if err != nil {
		return err
	}
	if t.origin < 0 {
		t.origin, t.offset = f.Timestamp, r.since()
	}
	hf.PTS = int64(f.Timestamp-t.origin+t.offset) * mediacodec.TimeScale / int64(time.Second)
	if err := t.w.WriteFrame(ctx, hf); err != nil {
		return err
	}
	t.frames.Add(1)
	return nil
}

// WriteAudio adds b (S16 samples in the microphone's format). It does
// nothing when the recording has no audio.
func (r *Recording) WriteAudio(ctx context.Context, b *audio.Buffer) error {
	switch {
	case r.wav != nil:
		if err := r.wav.Write(b); err != nil {
			return err
		}
		r.wav.dur.Store(int64(r.wav.Duration()))
		return nil
	case r.aac != nil:
		return r.aac.write(ctx, b, r.since)
	}
	return nil
}

// Frames is the number of video frames written.
func (r *Recording) Frames() int64 {
	if r.video == nil {
		return 0
	}
	return r.video.frames.Load()
}

// AudioDuration is the length of the audio written.
func (r *Recording) AudioDuration() time.Duration {
	switch {
	case r.wav != nil:
		return time.Duration(r.wav.dur.Load())
	case r.aac != nil:
		return time.Duration(r.aac.written.Load())
	}
	return 0
}

// Files lists the files the recording writes.
func (r *Recording) Files() []string { return r.Settings.Files() }

// Close flushes the encoders and finishes the file.
func (r *Recording) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var errs []error
	if t := r.video; t != nil {
		if err := t.w.Flush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("video: %w", err))
		}
		t.enc.Close()
		if err := t.dst.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if t := r.aac; t != nil {
		if err := t.finish(ctx); err != nil {
			errs = append(errs, fmt.Errorf("audio: %w", err))
		}
		t.enc.Close()
		if err := t.dst.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.wav != nil {
		if err := r.wav.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// abort releases what a failed StartRecording opened and removes its file.
func (r *Recording) abort() {
	if r.video != nil {
		r.video.enc.Close()
	}
	if r.aac != nil {
		r.aac.enc.Close()
	}
	if r.file != nil {
		r.file.Close()
		os.Remove(r.Settings.VideoPath())
	}
	if r.wav != nil {
		r.wav.Close()
		os.Remove(r.wav.Path)
	}
}

type videoTrack struct {
	enc    hwmediacodec.Encoder
	dst    hwmediacodec.PacketWriteCloser
	w      *hwmediacodec.EncodeWriter
	origin time.Duration // camera timestamp of the first frame
	offset time.Duration // where the first frame goes in the recording
	frames atomic.Int64
}

type aacTrack struct {
	enc      hwmediacodec.AudioEncoder
	dst      hwmediacodec.PacketWriteCloser
	rate, ch int
	started  bool
	samples  int64
	written  atomic.Int64 // ns
}

func (t *aacTrack) write(ctx context.Context, b *audio.Buffer, since func() time.Duration) error {
	if b.Format != audio.S16 || b.SampleRate != t.rate || b.Channels != t.ch {
		return fmt.Errorf("buffer is %v %d Hz %d ch, encoder takes S16 %d Hz %d ch", b.Format, b.SampleRate, b.Channels, t.rate, t.ch)
	}
	var pts int64
	if !t.started {
		// Only the first frame's PTS counts; the encoder continues from it.
		t.started = true
		pts = int64(since()) * int64(t.rate) / int64(time.Second)
	}
	f := hwmediacodec.S16Frame(b.S16(), t.rate, t.ch, pts)
	for {
		err := t.enc.Send(ctx, f)
		if errors.Is(err, hwmediacodec.ErrAgain) {
			if err := t.drain(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	t.samples += int64(b.Frames)
	t.written.Store(t.samples * int64(time.Second) / int64(t.rate))
	return t.drain(ctx)
}

// drain writes the packets the encoder has ready.
func (t *aacTrack) drain(ctx context.Context) error {
	for {
		p, err := t.enc.Receive(ctx)
		if errors.Is(err, hwmediacodec.ErrAgain) || errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := t.dst.WritePacket(p); err != nil {
			return err
		}
	}
}

func (t *aacTrack) finish(ctx context.Context) error {
	if !t.started {
		return nil
	}
	if err := t.enc.Flush(ctx); err != nil {
		return err
	}
	return t.drain(ctx)
}

// WAVWriter writes 16-bit PCM into a WAV file.
type WAVWriter struct {
	Path     string
	f        *os.File
	rate, ch int
	frames   int
	gaps     int
	dur      atomic.Int64 // Duration, for other goroutines
}

// CreateWAV creates path for audio at rate and ch channels.
func CreateWAV(path string, rate, ch int) (*WAVWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &WAVWriter{Path: path, f: f, rate: rate, ch: ch}
	if _, err := f.Write(wavHeader(rate, ch, 0)); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// Write appends b, which must hold S16 samples in the file's format.
func (w *WAVWriter) Write(b *audio.Buffer) error {
	if b.Format != audio.S16 || b.SampleRate != w.rate || b.Channels != w.ch {
		return fmt.Errorf("wav: buffer is %v %d Hz %d ch, file is S16 %d Hz %d ch", b.Format, b.SampleRate, b.Channels, w.rate, w.ch)
	}
	if b.Discontinuity {
		w.gaps++
	}
	if _, err := w.f.Write(b.Data); err != nil {
		return err
	}
	w.frames += b.Frames
	return nil
}

// Duration is the length written so far.
func (w *WAVWriter) Duration() time.Duration {
	return time.Duration(w.frames) * time.Second / time.Duration(w.rate)
}

// Gaps is the number of buffers that followed lost audio.
func (w *WAVWriter) Gaps() int { return w.gaps }

// Close writes the final sizes into the header and closes the file.
func (w *WAVWriter) Close() error {
	_, err := w.f.WriteAt(wavHeader(w.rate, w.ch, w.frames), 0)
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}

func wavHeader(rate, channels, frames int) []byte {
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
	return h
}

// Hint explains how to grant camera and microphone access.
func Hint(app string) string {
	switch runtime.GOOS {
	case "linux":
		return "hint: add yourself to the video group (sudo usermod -aG video $USER) and log in again"
	case "darwin":
		return "hint: allow " + app + " in System Settings > Privacy & Security > Camera / Microphone"
	case "windows":
		return "hint: turn on Settings > Privacy & security > Camera (and Microphone) > Let desktop apps access"
	}
	return ""
}

// Package session holds what the hwcamera GUI and hwcamera-cli share: the
// recording settings, their command-line flags, and the recorders.
//
// The GUI shows the hwcamera-cli command for its current settings. Both
// sides go through Settings.RecArgs and RegisterRecFlags, so the command
// it shows is the one the CLI parses back into the same settings.
package session

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Media says what a recording captures.
type Media int

const (
	Video Media = 1 << iota
	Audio
	Both = Video | Audio
)

func (m Media) String() string {
	switch m {
	case Video:
		return "video"
	case Audio:
		return "audio"
	case Both:
		return "video+audio"
	}
	return "none"
}

// ParseMedia reads "video", "audio" or "video+audio" (also "av", "both").
func ParseMedia(s string) (Media, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "video", "v":
		return Video, nil
	case "audio", "a":
		return Audio, nil
	case "video+audio", "audio+video", "av", "both":
		return Both, nil
	}
	return 0, fmt.Errorf("unknown media %q (video, audio or video+audio)", s)
}

func (m *Media) Set(s string) error {
	v, err := ParseMedia(s)
	if err == nil {
		*m = v
	}
	return err
}

// Size is a capture size; zero means the library's default (1280x720 or
// the nearest mode).
type Size struct{ W, H int }

func (s Size) String() string {
	if s.W == 0 && s.H == 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", s.W, s.H)
}

func (s *Size) Set(v string) error {
	if v == "" {
		*s = Size{}
		return nil
	}
	var w, h int
	if _, err := fmt.Sscanf(v, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return fmt.Errorf("bad size %q (want WxH, e.g. 1280x720)", v)
	}
	*s = Size{w, h}
	return nil
}

// Settings are the parameters of a recording.
type Settings struct {
	Media Media

	Camera string // camera ID; "" for the default camera
	Size   Size
	FPS    float64
	Codec  string // h264 or hevc
	// Bitrate of the video in bits per second.
	Bitrate int

	Mic      string // microphone ID; "" for the default input
	Rate     int
	Channels int

	// Output is the path of the recording without extension: video, with
	// the audio as an AAC track when both are recorded, goes to
	// Output+".mp4"; audio alone goes to Output+".wav".
	Output   string
	Duration time.Duration // 0: until stopped
}

// Defaults returns the settings hwcamera-cli rec uses without flags.
func Defaults() Settings {
	return Settings{
		Media:    Both,
		Size:     Size{1280, 720},
		FPS:      30,
		Codec:    "h264",
		Bitrate:  4_000_000,
		Rate:     48000,
		Channels: 1,
		Output:   "rec",
	}
}

// VideoPath and AudioPath are the files a recording writes.
func (s Settings) VideoPath() string { return s.Output + ".mp4" }
func (s Settings) AudioPath() string { return s.Output + ".wav" }

// Files lists the files the recording writes.
func (s Settings) Files() []string {
	if s.Media == Audio {
		return []string{s.AudioPath()}
	}
	return []string{s.VideoPath()}
}

type outputFlag struct{ p *string }

func (o outputFlag) String() string {
	if o.p == nil {
		return ""
	}
	return *o.p
}

// Set accepts the path with or without .mp4 / .wav.
func (o outputFlag) Set(v string) error {
	switch strings.ToLower(filepath.Ext(v)) {
	case ".mp4", ".wav":
		v = v[:len(v)-4]
	}
	if v == "" {
		return fmt.Errorf("empty output path")
	}
	*o.p = v
	return nil
}

// RegisterRecFlags adds the flags of hwcamera-cli rec, writing into s.
// The current values of s are the defaults.
func RegisterRecFlags(fs *flag.FlagSet, s *Settings) {
	fs.Var(&s.Media, "media", "what to record: video, audio or video+audio")
	fs.StringVar(&s.Camera, "camera", s.Camera, "camera ID (see list); empty for the default camera")
	fs.Var(&s.Size, "size", "capture size WxH (the nearest mode is used)")
	fs.Float64Var(&s.FPS, "fps", s.FPS, "frame rate")
	fs.StringVar(&s.Codec, "codec", s.Codec, "video codec: h264 or hevc")
	fs.IntVar(&s.Bitrate, "bitrate", s.Bitrate, "video bits per second")
	fs.StringVar(&s.Mic, "mic", s.Mic, "microphone ID (see mics); empty for the default input")
	fs.IntVar(&s.Rate, "rate", s.Rate, "audio sample rate")
	fs.IntVar(&s.Channels, "channels", s.Channels, "audio channels")
	fs.Var(outputFlag{&s.Output}, "o", "output path without extension: .mp4 for video (with AAC audio), .wav for audio alone")
	fs.DurationVar(&s.Duration, "duration", s.Duration, "length, e.g. 30s or 2m (0 = until Ctrl-C)")
}

// RecArgs returns the hwcamera-cli arguments that record with s: "rec"
// and the flags that differ from Defaults.
func (s Settings) RecArgs() []string {
	d := Defaults()
	args := []string{"rec"}
	add := func(name, v string) { args = append(args, "-"+name, v) }
	if s.Media != d.Media {
		add("media", s.Media.String())
	}
	if s.Media&Video != 0 {
		if s.Camera != "" {
			add("camera", s.Camera)
		}
		if s.Size != d.Size {
			add("size", s.Size.String())
		}
		if s.FPS != d.FPS {
			add("fps", strconv.FormatFloat(s.FPS, 'g', -1, 64))
		}
		if s.Codec != d.Codec {
			add("codec", s.Codec)
		}
		if s.Bitrate != d.Bitrate {
			add("bitrate", strconv.Itoa(s.Bitrate))
		}
	}
	if s.Media&Audio != 0 {
		if s.Mic != "" {
			add("mic", s.Mic)
		}
		if s.Rate != d.Rate {
			add("rate", strconv.Itoa(s.Rate))
		}
		if s.Channels != d.Channels {
			add("channels", strconv.Itoa(s.Channels))
		}
	}
	if s.Output != d.Output {
		add("o", s.Output)
	}
	if s.Duration != d.Duration {
		add("duration", s.Duration.String())
	}
	return args
}

// SnapArgs returns the hwcamera-cli arguments that take the snapshot the
// GUI takes.
func SnapArgs(camera string, size Size, out string) []string {
	args := []string{"snap"}
	if camera != "" {
		args = append(args, "-camera", camera)
	}
	if size != (Size{}) {
		args = append(args, "-size", size.String())
	}
	return append(args, "-o", out)
}

// Command renders hwcamera-cli and args as a line for a POSIX shell (and
// PowerShell, which takes the same single quotes).
func Command(args []string) string {
	parts := []string{"hwcamera-cli"}
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=+,@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// OutputDir is where the GUI saves by default: the user's Movies (macOS)
// or Videos folder, else the home directory, else the working directory.
func OutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	for _, sub := range []string{"Movies", "Videos"} {
		if st, err := os.Stat(filepath.Join(home, sub)); err == nil && st.IsDir() {
			return filepath.Join(home, sub)
		}
	}
	return home
}

// Stamp names a recording or a snapshot after the current time.
func Stamp(prefix string) string {
	return prefix + "-" + time.Now().Format("20060102-150405")
}

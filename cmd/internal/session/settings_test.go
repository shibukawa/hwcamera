package session

import (
	"flag"
	"io"
	"testing"
	"time"
)

// The GUI shows Command(s.RecArgs()); hwcamera-cli must parse it back
// into the same settings.
func TestRecArgsRoundTrip(t *testing.T) {
	cases := []Settings{
		Defaults(),
		func() Settings {
			s := Defaults()
			s.Media = Video
			s.Camera = "FDF90FEB-59E5"
			s.Size = Size{1920, 1080}
			s.FPS = 60
			s.Codec = "hevc"
			s.Bitrate = 8_000_000
			s.Output = "/Users/me/Movies/my clip"
			s.Duration = 30 * time.Second
			return s
		}(),
		func() Settings {
			s := Defaults()
			s.Media = Audio
			s.Mic = "BuiltInMicrophoneDevice"
			s.Rate = 44100
			s.Channels = 2
			s.Output = "voice"
			return s
		}(),
	}
	for _, want := range cases {
		args := want.RecArgs()
		if args[0] != "rec" {
			t.Fatalf("args %q do not start with rec", args)
		}
		got := Defaults()
		fs := flag.NewFlagSet("rec", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		RegisterRecFlags(fs, &got)
		if err := fs.Parse(args[1:]); err != nil {
			t.Fatalf("parse %q: %v", args, err)
		}
		// Fields of the track not recorded are not in the command.
		if want.Media&Video == 0 {
			got.Camera, got.Size, got.FPS, got.Codec, got.Bitrate = want.Camera, want.Size, want.FPS, want.Codec, want.Bitrate
		}
		if want.Media&Audio == 0 {
			got.Mic, got.Rate, got.Channels = want.Mic, want.Rate, want.Channels
		}
		if got != want {
			t.Errorf("%s\n got  %+v\n want %+v", Command(args), got, want)
		}
	}
}

func TestOutputFlagStripsExtension(t *testing.T) {
	s := Defaults()
	fs := flag.NewFlagSet("rec", flag.ContinueOnError)
	RegisterRecFlags(fs, &s)
	if err := fs.Parse([]string{"-o", "clip.mp4"}); err != nil {
		t.Fatal(err)
	}
	if s.VideoPath() != "clip.mp4" || s.AudioPath() != "clip.wav" {
		t.Errorf("paths %s, %s", s.VideoPath(), s.AudioPath())
	}
}

func TestCommandQuoting(t *testing.T) {
	got := Command([]string{"rec", "-o", "/tmp/my clip", "-mic", "it's"})
	want := `hwcamera-cli rec -o '/tmp/my clip' -mic 'it'"'"'s'`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

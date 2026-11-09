package camera

import "testing"

var uvc = []Mode{
	{Format: MJPEG, Width: 1920, Height: 1080, FrameRates: []float64{30}},
	{Format: MJPEG, Width: 1280, Height: 720, FrameRates: []float64{30}},
	{Format: YUYV, Width: 1920, Height: 1080, FrameRates: []float64{5}},
	{Format: YUYV, Width: 1280, Height: 720, FrameRates: []float64{10}},
	{Format: YUYV, Width: 640, Height: 480, FrameRates: []float64{30, 15}},
}

func TestChooseMode(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  Config
		want Mode
	}{
		// 720p30 is only reachable as MJPEG on a USB 2 camera.
		{"default", Config{Format: NV12}, uvc[1]},
		{"vga", Config{Width: 640, Height: 480, Format: NV12}, uvc[4]},
		{"720p at 10fps prefers raw", Config{Width: 1280, Height: 720, FrameRate: 10, Format: NV12}, uvc[3]},
		{"forced yuyv", Config{Width: 1920, Height: 1080, Native: YUYV}, uvc[2]},
		{"odd size rounds up", Config{Width: 800, Height: 600, Format: NV12}, uvc[1]},
		{"too large takes largest", Config{Width: 4000, Height: 3000, Format: NV12}, uvc[0]},
	} {
		got, err := ChooseMode(uvc, c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != c.want.String() {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	if _, err := ChooseMode(uvc, Config{Native: NV12}); err == nil {
		t.Error("missing native format accepted")
	}
}

func TestPickFrameRate(t *testing.T) {
	m := Mode{FrameRates: []float64{60, 30, 15}}
	if r := PickFrameRate(m, 0); r != 30 {
		t.Errorf("default = %v", r)
	}
	if r := PickFrameRate(m, 25); r != 30 {
		t.Errorf("25 -> %v, want 30", r)
	}
	if r := PickFrameRate(m, 100); r != 60 {
		t.Errorf("100 -> %v, want 60", r)
	}
}

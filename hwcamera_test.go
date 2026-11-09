package hwcamera

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/pixconv"
)

// fakeBackend delivers YUYV frames from driver-owned buffers, the way V4L2
// does, so the public package's conversion and release paths can be tested
// without a camera.
type fakeBackend struct{}

func (fakeBackend) Name() string    { return "fake" }
func (fakeBackend) Available() bool { return true }
func (fakeBackend) Devices(context.Context) ([]camera.DeviceInfo, error) {
	modes := []camera.Mode{{Format: camera.YUYV, Width: 64, Height: 48, FrameRates: []float64{30}}}
	// The virtual camera comes first, as macOS listed Immersed Camera
	// before an iPhone: Open("") must still pick the physical one.
	return []camera.DeviceInfo{
		{ID: "vcam", Name: "Virtual", Backend: "fake", Virtual: true, Modes: modes},
		{ID: "fake0", Name: "Fake", Backend: "fake", Position: camera.PositionExternal, Modes: modes},
	}, nil
}

func (fakeBackend) Open(_ context.Context, id string, cfg camera.Config) (camera.Stream, error) {
	ds, _ := fakeBackend{}.Devices(context.Background())
	m, err := camera.ChooseMode(ds[1].Modes, cfg)
	if err != nil {
		return nil, err
	}
	return &fakeStream{mode: m}, nil
}

type fakeStream struct {
	mode     camera.Mode
	mu       sync.Mutex
	seq      uint64
	out      int
	closed   bool
	released int
}

func (s *fakeStream) Mode() camera.Mode { return s.mode }
func (s *fakeStream) Read(ctx context.Context) (*camera.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, camera.ErrClosed
	}
	f := pixconv.NewFrame(camera.YUYV, s.mode.Width, s.mode.Height)
	for i := 0; i < len(f.Planes[0]); i += 4 {
		f.Planes[0][i], f.Planes[0][i+1], f.Planes[0][i+2], f.Planes[0][i+3] = 81, 90, 81, 240 // red
	}
	f.Sequence = s.seq
	f.Timestamp = time.Duration(s.seq) * time.Second / 30
	s.seq++
	s.out++
	camera.SetRelease(f, func() {
		s.mu.Lock()
		s.out--
		s.released++
		s.mu.Unlock()
	})
	return f, nil
}
func (s *fakeStream) Close() error { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }

func init() { camera.Register(fakeBackend{}) }

func TestOpenConvertsAndReleases(t *testing.T) {
	t.Setenv("HWCAMERA_BACKEND", "fake")
	ctx := context.Background()
	for _, format := range []PixelFormat{NV12, RGBA, Any} {
		cam, err := Open(ctx, "", WithFormat(format))
		if err != nil {
			t.Fatal(err)
		}
		if cam.Info().ID != "fake0" {
			t.Fatalf("Open(\"\") picked %s, want the physical camera", cam.Info().ID)
		}
		f, err := cam.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := format
		if format == Any {
			want = YUYV
		}
		if f.Format != want || cam.Format() != want {
			t.Fatalf("got %s, want %s", f.Format, want)
		}
		if format == RGBA {
			p := f.Planes[0]
			if p[0] < 230 || p[1] > 20 || p[2] > 20 {
				t.Errorf("red came out as %v", p[:4])
			}
		}
		f.Release()
		s := cam.stream.(*fakeStream)
		if s.out != 0 {
			t.Errorf("%s: %d driver frames still held", format, s.out)
		}
		cam.Close()
		if _, err := cam.Read(ctx); !errors.Is(err, ErrClosed) {
			t.Errorf("read after close: %v", err)
		}
	}
}

func TestOpenUnknownID(t *testing.T) {
	t.Setenv("HWCAMERA_BACKEND", "fake")
	if _, err := Open(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenWithKind(t *testing.T) {
	t.Setenv("HWCAMERA_BACKEND", "fake")
	ctx := context.Background()
	for _, c := range []struct {
		id   string
		kind Kind
		want string // device ID, or "" for ErrNotFound
	}{
		{"", External, "fake0"},
		{"", External | Physical, "fake0"},
		{"", Virtual, "vcam"},
		{"", Front, ""},
		{"", External | Virtual, ""},
		{"fake0", External, "fake0"},
		{"fake0", Front, ""},
		{"vcam", Physical, ""},
	} {
		cam, err := Open(ctx, c.id, WithKind(c.kind))
		if c.want == "" {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%q %s: err = %v, want ErrNotFound", c.id, c.kind, err)
			}
			if cam != nil {
				cam.Close()
			}
			continue
		}
		if err != nil {
			t.Errorf("%q %s: %v", c.id, c.kind, err)
			continue
		}
		if got := cam.Info().ID; got != c.want {
			t.Errorf("%q %s: opened %s, want %s", c.id, c.kind, got, c.want)
		}
		cam.Close()
	}
}

// Package camgame is the camera and microphone sample as an Ebitengine
// game, shared by the mobile app (examples/mobile, bound with ebitenmobile)
// and its desktop runner (examples/mobile/desktop).
//
// It shows the camera full screen, a level meter of the microphone, and
// buttons to switch the camera, mirror, rotate and mute. Opening a device
// is retried every second while the permission is missing, because on
// Android the app asks for it from Java after the game has started.
package camgame

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/audio"
)

// Game is the sample. Create it with New.
type Game struct {
	w, h int

	devices []hwcamera.DeviceInfo
	index   int
	cam     *capture
	img     *ebiten.Image
	camErr  error
	retryAt time.Time

	mirror bool
	rotate int // quarter turns clockwise

	mic       *micMeter
	micErr    error
	micOn     bool
	micRetry  time.Time
	buttons   []button
	listedAt  time.Time
	lastTouch time.Time
}

// New returns the game; devices are opened on the first Update so that
// errors show on screen instead of aborting the application.
func New() *Game { return &Game{micOn: true} }

type button struct {
	label string
	r     image.Rectangle
	do    func()
}

// --- camera ---------------------------------------------------------------

type capture struct {
	cam    *hwcamera.Camera
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	latest *hwcamera.Frame
	err    error
	frames int
	start  time.Time
}

func openCapture(id string) (*capture, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cam, err := hwcamera.Open(ctx, id, hwcamera.WithSize(1280, 720), hwcamera.WithFrameRate(30), hwcamera.WithFormat(hwcamera.RGBA))
	if err != nil {
		cancel()
		return nil, err
	}
	c := &capture{cam: cam, cancel: cancel, done: make(chan struct{}), start: time.Now()}
	go func() {
		defer close(c.done)
		for {
			f, err := cam.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					c.mu.Lock()
					c.err = err
					c.mu.Unlock()
				}
				return
			}
			c.mu.Lock()
			if c.latest != nil {
				c.latest.Release()
			}
			c.latest = f
			c.frames++
			c.mu.Unlock()
		}
	}()
	return c, nil
}

func (c *capture) take() (*hwcamera.Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.latest
	c.latest = nil
	return f, c.err
}

func (c *capture) fps() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return float64(c.frames) / time.Since(c.start).Seconds()
}

func (c *capture) close() {
	c.cancel()
	c.cam.Close()
	<-c.done
	if f, _ := c.take(); f != nil {
		f.Release()
	}
}

// --- microphone -----------------------------------------------------------

type micMeter struct {
	mic    *audio.Mic
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	peak   float64 // decaying
	rms    float64
	err    error
	gaps   int
	frames int
}

func openMic() (*micMeter, error) {
	ctx, cancel := context.WithCancel(context.Background())
	mic, err := audio.Open(ctx, "")
	if err != nil {
		cancel()
		return nil, err
	}
	m := &micMeter{mic: mic, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(m.done)
		for {
			b, err := mic.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					m.mu.Lock()
					m.err = err
					m.mu.Unlock()
				}
				return
			}
			peak, rms := b.Level()
			m.mu.Lock()
			m.peak = math.Max(peak, m.peak*0.9)
			m.rms = rms
			m.frames += b.Frames
			if b.Discontinuity {
				m.gaps++
			}
			m.mu.Unlock()
		}
	}()
	return m, nil
}

func (m *micMeter) level() (peak, rms float64, seconds float64, gaps int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak, m.rms, float64(m.frames) / float64(m.mic.SampleRate()), m.gaps, m.err
}

func (m *micMeter) close() {
	m.cancel()
	m.mic.Close()
	<-m.done
}

// --- game -----------------------------------------------------------------

func (g *Game) openCamera(i int) {
	if g.cam != nil {
		g.cam.close()
		g.cam = nil
	}
	g.camErr = nil
	if len(g.devices) == 0 {
		return
	}
	g.index = i
	d := g.devices[i]
	c, err := openCapture(d.ID)
	if err != nil {
		g.camErr = err
		g.retryAt = time.Now().Add(time.Second)
		return
	}
	g.cam = c
	g.mirror = d.Position == hwcamera.PositionFront
	w, h := c.cam.Size()
	if g.img == nil || g.img.Bounds().Dx() != w || g.img.Bounds().Dy() != h {
		g.img = ebiten.NewImage(w, h)
	}
}

func (g *Game) listDevices() {
	g.listedAt = time.Now()
	ds, err := hwcamera.Devices(context.Background())
	if err != nil {
		g.camErr = err
		g.retryAt = time.Now().Add(time.Second)
		return
	}
	g.devices = ds
	start := 0
	for i, d := range ds {
		if !d.Virtual {
			start = i
			break
		}
	}
	g.openCamera(start)
}

func (g *Game) toggleMic() {
	g.micOn = !g.micOn
	if !g.micOn && g.mic != nil {
		g.mic.close()
		g.mic = nil
	}
	g.micErr = nil
	g.micRetry = time.Time{}
}

// retryable reports errors worth retrying: the permission may be granted
// later (Android asks from Java), and a busy device may be freed.
func retryable(err error) bool {
	return errors.Is(err, hwcamera.ErrPermission) || errors.Is(err, hwcamera.ErrBusy) ||
		errors.Is(err, audio.ErrPermission) || errors.Is(err, audio.ErrBusy) ||
		errors.Is(err, hwcamera.ErrNoDevice)
}

func (g *Game) Update() error {
	now := time.Now()
	if g.listedAt.IsZero() || (g.cam == nil && g.camErr != nil && retryable(g.camErr) && now.After(g.retryAt)) {
		if len(g.devices) == 0 {
			g.listDevices()
		} else {
			g.openCamera(g.index)
		}
	}
	if g.micOn && g.mic == nil && (g.micErr == nil || (retryable(g.micErr) && now.After(g.micRetry))) {
		m, err := openMic()
		if err != nil {
			g.micErr = err
			g.micRetry = now.Add(time.Second)
		} else {
			g.mic, g.micErr = m, nil
		}
	}

	if g.cam != nil {
		f, err := g.cam.take()
		if f != nil {
			if f.Strides[0] == 4*f.Width && g.img.Bounds().Dx() == f.Width && g.img.Bounds().Dy() == f.Height {
				g.img.WritePixels(f.Planes[0])
			}
			f.Release()
		}
		if err != nil {
			g.camErr = err
		}
	}
	g.layoutButtons()
	g.handleInput()
	return nil
}

func (g *Game) layoutButtons() {
	labels := []struct {
		l  string
		do func()
	}{
		{"Camera", func() {
			if len(g.devices) > 0 {
				g.openCamera((g.index + 1) % len(g.devices))
			}
		}},
		{"Mirror", func() { g.mirror = !g.mirror }},
		{"Rotate", func() { g.rotate = (g.rotate + 1) % 4 }},
		{map[bool]string{true: "Mic off", false: "Mic on"}[g.micOn], g.toggleMic},
	}
	bw := min(g.w/len(labels), 160)
	bh := 44
	x0 := (g.w - bw*len(labels)) / 2
	y := g.h - bh - 24
	g.buttons = g.buttons[:0]
	for i, l := range labels {
		r := image.Rect(x0+i*bw+4, y, x0+(i+1)*bw-4, y+bh)
		g.buttons = append(g.buttons, button{label: l.l, r: r, do: l.do})
	}
}

func (g *Game) handleInput() {
	var taps []image.Point
	for _, id := range inpututil.AppendJustPressedTouchIDs(nil) {
		x, y := ebiten.TouchPosition(id)
		taps = append(taps, image.Pt(x, y))
		g.lastTouch = time.Now()
	}
	// Mouse for the desktop runner; touch screens also synthesize mouse
	// events on some platforms, so ignore the mouse right after a touch.
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) && time.Since(g.lastTouch) > 500*time.Millisecond {
		x, y := ebiten.CursorPosition()
		taps = append(taps, image.Pt(x, y))
	}
	for _, p := range taps {
		for _, b := range g.buttons {
			if p.In(b.r) {
				b.do()
			}
		}
	}
}

var (
	buttonColor = color.RGBA{0x20, 0x20, 0x20, 0xc0}
	meterBack   = color.RGBA{0x30, 0x30, 0x30, 0xc0}
	meterRMS    = color.RGBA{0x30, 0xc0, 0x60, 0xff}
	meterPeak   = color.RGBA{0xf0, 0xd0, 0x30, 0xff}
)

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.Black)
	if g.img != nil && g.cam != nil {
		g.drawPicture(screen)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "hwcamera  %s/%s\n", runtime.GOOS, runtime.GOARCH)
	switch {
	case len(g.devices) > 0:
		d := g.devices[g.index]
		fmt.Fprintf(&b, "[%d/%d] %s (%s, %s)\n", g.index+1, len(g.devices), d.Name, d.Backend, d.Position)
	}
	if g.cam != nil {
		fmt.Fprintf(&b, "%s -> %s  %.1f fps\n", g.cam.cam.Mode(), g.cam.cam.Format(), g.cam.fps())
	}
	if g.camErr != nil {
		fmt.Fprintf(&b, "camera: %v\n", g.camErr)
	}
	if g.mic != nil {
		peak, rms, sec, gaps, err := g.mic.level()
		fmt.Fprintf(&b, "mic: %s (%s) %d Hz  %.0fs  %d gaps\n", g.mic.mic.Info().Name, g.mic.mic.Info().Backend, g.mic.mic.SampleRate(), sec, gaps)
		if err != nil {
			fmt.Fprintf(&b, "mic: %v\n", err)
		}
		g.drawMeter(screen, peak, rms)
	} else if g.micErr != nil {
		fmt.Fprintf(&b, "mic: %v\n", g.micErr)
	} else if !g.micOn {
		b.WriteString("mic: off\n")
	}
	drawText(screen, b.String(), 12, textTop)
	for _, bt := range g.buttons {
		vector.FillRect(screen, float32(bt.r.Min.X), float32(bt.r.Min.Y), float32(bt.r.Dx()), float32(bt.r.Dy()), buttonColor, false)
		tx := bt.r.Min.X + (bt.r.Dx()-6*len(bt.label))/2
		ebitenutil.DebugPrintAt(screen, bt.label, tx, bt.r.Min.Y+bt.r.Dy()/2-8)
	}
}

// textTop keeps the text clear of the notch or Dynamic Island of recent
// iPhones (Ebitengine does not report the safe area).
const textTop = 56

func drawText(screen *ebiten.Image, s string, x, y int) {
	for i, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		w := 6*len(line) + 6
		vector.FillRect(screen, float32(x-3), float32(y+i*16), float32(w), 16, color.RGBA{0, 0, 0, 0x90}, false)
		ebitenutil.DebugPrintAt(screen, line, x, y+i*16)
	}
}

func (g *Game) drawMeter(screen *ebiten.Image, peak, rms float64) {
	// dBFS from -60 to 0 maps to the bar length.
	scale := func(v float64) float32 {
		if v <= 0 {
			return 0
		}
		db := 20 * math.Log10(v)
		return float32(math.Max(0, math.Min(1, (db+60)/60)))
	}
	w := float32(g.w - 48)
	y := float32(g.h - 44 - 24 - 20)
	vector.FillRect(screen, 24, y, w, 10, meterBack, false)
	vector.FillRect(screen, 24, y, w*scale(rms), 10, meterRMS, false)
	px := 24 + w*scale(peak)
	vector.FillRect(screen, px-2, y-2, 4, 14, meterPeak, false)
}

func (g *Game) drawPicture(screen *ebiten.Image) {
	iw, ih := float64(g.img.Bounds().Dx()), float64(g.img.Bounds().Dy())
	op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
	// Centre, mirror, rotate, then fit into the screen.
	op.GeoM.Translate(-iw/2, -ih/2)
	if g.mirror {
		op.GeoM.Scale(-1, 1)
	}
	op.GeoM.Rotate(float64(g.rotate) * math.Pi / 2)
	rw, rh := iw, ih
	if g.rotate%2 == 1 {
		rw, rh = ih, iw
	}
	k := math.Min(float64(g.w)/rw, float64(g.h)/rh)
	op.GeoM.Scale(k, k)
	op.GeoM.Translate(float64(g.w)/2, float64(g.h)/2)
	screen.DrawImage(g.img, op)
}

func (g *Game) Layout(ow, oh int) (int, int) {
	g.w, g.h = ow, oh
	return ow, oh
}

// Close releases the devices (desktop runner).
func (g *Game) Close() {
	if g.cam != nil {
		g.cam.close()
	}
	if g.mic != nil {
		g.mic.close()
	}
}

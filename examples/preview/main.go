// Command preview shows a camera in an Ebitengine window:
//
//	go run ./preview
//	go run ./preview -device <ID> -size 1920x1080 -fps 60
//
// Keys:
//
//	Tab        next camera (virtual cameras included)
//	M          mirror on/off (on by default for front cameras)
//	S          save the current frame as preview-<time>.png
//	F          fullscreen on/off
//	H          hide/show the overlay
//	Esc / Q    quit
//
// Frames are read on a goroutine as RGBA (hwcamera converts from the
// camera's NV12, YUYV or MJPEG) and the newest one is uploaded to an
// ebiten.Image in Update, so a slow camera never stalls the window and a
// fast one never queues up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/shibukawa/hwcamera"
)

type capture struct {
	cam    *hwcamera.Camera
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	latest *hwcamera.Frame // newest frame not yet shown; owned by the capture until taken
	err    error
	frames int
	drops  int // frames replaced before the window took them
	start  time.Time
}

func startCapture(id string, w, h int, fps float64) (*capture, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cam, err := hwcamera.Open(ctx, id, hwcamera.WithSize(w, h), hwcamera.WithFrameRate(fps), hwcamera.WithFormat(hwcamera.RGBA))
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
				if ctx.Err() == nil && !errors.Is(err, hwcamera.ErrClosed) {
					c.mu.Lock()
					c.err = err
					c.mu.Unlock()
				}
				return
			}
			c.mu.Lock()
			if c.latest != nil {
				c.latest.Release()
				c.drops++
			}
			c.latest = f
			c.frames++
			c.mu.Unlock()
		}
	}()
	return c, nil
}

// take returns the newest frame, or nil when none arrived since the last call.
func (c *capture) take() *hwcamera.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.latest
	c.latest = nil
	return f
}

func (c *capture) stats() (frames, drops int, fps float64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames, c.drops, float64(c.frames) / time.Since(c.start).Seconds(), c.err
}

func (c *capture) stop() {
	c.cancel()
	c.cam.Close()
	<-c.done
	if f := c.take(); f != nil {
		f.Release()
	}
}

type game struct {
	devices []hwcamera.DeviceInfo
	index   int
	w, h    int
	fps     float64

	cap     *capture
	img     *ebiten.Image
	mirror  bool
	overlay bool
	status  string // last message (snapshot saved, open error)

	quitAfter time.Duration
	shot      string
	shotDue   bool
	shotDone  bool
	began     time.Time
	last      *image.RGBA // the frame on screen, for S
}

func (g *game) open(i int) {
	if g.cap != nil {
		g.cap.stop()
		g.cap = nil
	}
	g.index = i
	d := g.devices[i]
	c, err := startCapture(d.ID, g.w, g.h, g.fps)
	if err != nil {
		g.status = fmt.Sprintf("%s: %v", d.Name, err)
		return
	}
	g.cap = c
	g.mirror = d.Position == hwcamera.PositionFront
	g.status = ""
	cw, ch := c.cam.Size()
	if g.img == nil || g.img.Bounds().Dx() != cw || g.img.Bounds().Dy() != ch {
		g.img = ebiten.NewImage(cw, ch)
		g.last = nil
	}
	ebiten.SetWindowTitle(fmt.Sprintf("hwcamera preview - %s", d.Name))
}

func (g *game) Update() error {
	if g.began.IsZero() {
		g.began = time.Now()
	}
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape), inpututil.IsKeyJustPressed(ebiten.KeyQ):
		return ebiten.Termination
	case inpututil.IsKeyJustPressed(ebiten.KeyTab):
		g.open((g.index + 1) % len(g.devices))
	case inpututil.IsKeyJustPressed(ebiten.KeyM):
		g.mirror = !g.mirror
	case inpututil.IsKeyJustPressed(ebiten.KeyH):
		g.overlay = !g.overlay
	case inpututil.IsKeyJustPressed(ebiten.KeyF):
		ebiten.SetFullscreen(!ebiten.IsFullscreen())
	case inpututil.IsKeyJustPressed(ebiten.KeyS):
		g.status = g.save(fmt.Sprintf("preview-%s.png", time.Now().Format("20060102-150405")))
	}
	if g.cap != nil {
		if f := g.cap.take(); f != nil {
			// RGBA frames from hwcamera have rows of exactly 4*Width
			// bytes, which is what WritePixels takes.
			if f.Strides[0] == 4*f.Width && g.img.Bounds().Dx() == f.Width && g.img.Bounds().Dy() == f.Height {
				g.img.WritePixels(f.Planes[0])
				if g.last == nil {
					g.last = image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
				}
				copy(g.last.Pix, f.Planes[0])
			}
			f.Release()
		}
	}
	if g.quitAfter > 0 && time.Since(g.began) > g.quitAfter {
		if g.shot == "" || g.shotDone {
			return ebiten.Termination
		}
		g.shotDue = true // Draw saves the window, then the next Update quits
	}
	return nil
}

func (g *game) save(path string) string {
	if g.last == nil {
		return "no frame yet"
	}
	f, err := os.Create(path)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	if err := png.Encode(f, g.last); err != nil {
		return err.Error()
	}
	return "saved " + path
}

func (g *game) Draw(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	if g.img != nil {
		iw, ih := g.img.Bounds().Dx(), g.img.Bounds().Dy()
		// Fit the picture into the window, keeping its aspect ratio.
		k := min(float64(sw)/float64(iw), float64(sh)/float64(ih))
		op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
		if g.mirror {
			op.GeoM.Scale(-1, 1)
			op.GeoM.Translate(float64(iw), 0)
		}
		op.GeoM.Scale(k, k)
		op.GeoM.Translate((float64(sw)-float64(iw)*k)/2, (float64(sh)-float64(ih)*k)/2)
		screen.DrawImage(g.img, op)
	}
	if g.overlay {
		g.drawOverlay(screen)
	}
	g.maybeShoot(screen)
}

func (g *game) drawOverlay(screen *ebiten.Image) {
	d := g.devices[g.index]
	var b strings.Builder
	fmt.Fprintf(&b, "[%d/%d] %s (%s", g.index+1, len(g.devices), d.Name, d.Backend)
	if d.Virtual {
		b.WriteString(", virtual")
	}
	b.WriteString(")\n")
	if g.cap != nil {
		frames, drops, fps, err := g.cap.stats()
		fmt.Fprintf(&b, "%s -> %s  camera %.1f fps  shown %d/%d  window %.0f fps\n",
			g.cap.cam.Mode(), g.cap.cam.Format(), fps, frames-drops, frames, ebiten.ActualFPS())
		if err != nil {
			fmt.Fprintf(&b, "error: %v\n", err)
		}
	}
	if g.status != "" {
		b.WriteString(g.status + "\n")
	}
	mirror := "off"
	if g.mirror {
		mirror = "on"
	}
	fmt.Fprintf(&b, "Tab: next camera  M: mirror (%s)  S: save  F: fullscreen  H: hide  Q: quit", mirror)
	ebitenutil.DebugPrint(screen, b.String())
}

// maybeShoot saves the rendered window for -shot.
func (g *game) maybeShoot(screen *ebiten.Image) {
	if g.shotDue && !g.shotDone {
		g.shotDone = true
		img := image.NewRGBA(screen.Bounds())
		screen.ReadPixels(img.Pix)
		if f, err := os.Create(g.shot); err == nil {
			png.Encode(f, img)
			f.Close()
			fmt.Println("saved window to", g.shot)
		}
	}
}

func (g *game) Layout(ow, oh int) (int, int) { return ow, oh }

func main() {
	device := flag.String("device", "", "camera ID (hwcamera list); empty for the default camera")
	size := flag.String("size", "1280x720", "capture size")
	fps := flag.Float64("fps", 30, "capture frame rate")
	quitAfter := flag.Duration("quit-after", 0, "quit after this long (for scripted checks)")
	shot := flag.String("shot", "", "with -quit-after: save the window to this PNG before quitting")
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil {
		log.Fatalf("bad -size %q", *size)
	}
	devices, err := hwcamera.Devices(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if len(devices) == 0 {
		log.Fatal("no cameras")
	}
	g := &game{devices: devices, w: w, h: h, fps: *fps, overlay: true, quitAfter: *quitAfter, shot: *shot}
	start := 0
	for i, d := range devices {
		if d.ID == *device {
			start = i
		}
	}
	if *device == "" {
		// Devices puts the default and physical cameras first; skip
		// leading virtual ones like Open("") does.
		for i, d := range devices {
			if !d.Virtual {
				start = i
				break
			}
		}
	}
	g.open(start)
	defer func() {
		if g.cap != nil {
			g.cap.stop()
		}
	}()

	ebiten.SetWindowSize(1280, 720)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	if err := ebiten.RunGame(g); err != nil {
		log.Fatal(err)
	}
}

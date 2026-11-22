// Command desktop runs the mobile sample's game in a desktop window, to try
// the screen without a phone:
//
//	go run ./mobile/desktop
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/shibukawa/hwcamera/examples/internal/camgame"
)

// runner adds the scripted-check flags around the game.
type runner struct {
	*camgame.Game
	quitAfter time.Duration
	shot      string
	start     time.Time
	saved     bool
}

func (r *runner) Update() error {
	if r.start.IsZero() {
		r.start = time.Now()
	}
	if r.quitAfter > 0 && time.Since(r.start) > r.quitAfter && (r.shot == "" || r.saved) {
		return ebiten.Termination
	}
	return r.Game.Update()
}

func (r *runner) Draw(screen *ebiten.Image) {
	r.Game.Draw(screen)
	if r.quitAfter > 0 && r.shot != "" && !r.saved && time.Since(r.start) > r.quitAfter {
		img := image.NewRGBA(screen.Bounds())
		screen.ReadPixels(img.Pix)
		if f, err := os.Create(r.shot); err == nil {
			png.Encode(f, img)
			f.Close()
			fmt.Println("saved window to", r.shot)
		}
		r.saved = true
	}
}

func main() {
	quitAfter := flag.Duration("quit-after", 0, "quit after this long (for scripted checks)")
	shot := flag.String("shot", "", "with -quit-after: save the window to this PNG before quitting")
	flag.Parse()
	g := camgame.New()
	defer g.Close()
	ebiten.SetWindowSize(480, 860) // a phone held upright
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle("hwcamera mobile sample")
	if err := ebiten.RunGame(&runner{Game: g, quitAfter: *quitAfter, shot: *shot}); err != nil {
		log.Fatal(err)
	}
}

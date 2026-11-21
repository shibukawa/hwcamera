// Command hwcamera is a window for the cameras and microphones of the
// machine: live preview, switching devices and modes, snapshots, and
// recording video (hardware H.264 / HEVC into MP4), audio (WAV) or both.
//
//	go install github.com/shibukawa/hwcamera/cmd/hwcamera@latest
//	hwcamera [-camera ID] [-mic ID] [-o DIR]
//
// For everything it does, the window shows the matching hwcamera-cli
// command, the scriptable side:
//
//	go install github.com/shibukawa/hwcamera/cmd/hwcamera-cli@latest
//	hwcamera-cli rec -duration 10s -o clip   # clip.mp4 with H.264 + AAC
//
// Video and audio go into one MP4 (hardware H.264 / HEVC and AAC); audio
// alone goes into a WAV file.
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
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ebitengine/debugui"
	"github.com/hajimehoshi/ebiten/v2"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/audio"
	"github.com/shibukawa/hwcamera/cmd/internal/session"
)

const panelWidth = 400 // width of the control panel, in window points

var (
	mediaChoices = []session.Media{session.Both, session.Video, session.Audio}
	mediaNames   = []string{"Video + audio", "Video only", "Audio only"}
	codecChoices = []string{"h264", "hevc"}
	codecNames   = []string{"H.264", "HEVC"}
)

// mode is one entry of the mode menu: a size at a frame rate.
type mode struct {
	size session.Size
	fps  float64
}

type app struct {
	ui debugui.DebugUI

	cams     []hwcamera.DeviceInfo
	camNames []string
	camIdx   int
	modes    []mode
	modeKeys []string
	modeIdx  int

	mics     []audio.DeviceInfo
	micNames []string
	micIdx   int

	mediaIdx int
	codecIdx int
	output   string // recording path without extension
	mirror   bool

	cam   *capture
	mic   *micCapture
	img   *ebiten.Image
	last  *image.RGBA // the frame on screen, for snapshots
	scale float64

	rec      *session.Recording // nil when not recording
	recStart time.Time

	status   string
	snapPath string // last snapshot, for the snap command

	// selections the devices were opened with, to notice changes
	openCam, openMode, openMic int

	quitAfter time.Duration
	shot      string
	began     time.Time
	shotDone  bool
}

func main() {
	camID := flag.String("camera", "", "camera ID to start with (hwcamera-cli list)")
	micID := flag.String("mic", "", "microphone ID to start with (hwcamera-cli mics)")
	dir := flag.String("o", session.OutputDir(), "folder for recordings and snapshots")
	quitAfter := flag.Duration("quit-after", 0, "quit after this long (for scripted checks)")
	shot := flag.String("shot", "", "with -quit-after: save the window to this PNG before quitting")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: hwcamera [-camera ID] [-mic ID] [-o DIR]\n\nOpens a window with the camera preview, device switching and recording.\nThe command-line tool is hwcamera-cli (go install github.com/shibukawa/hwcamera/cmd/hwcamera-cli@latest).\n\nflags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	a := &app{openCam: -1, openMode: -1, openMic: -1, quitAfter: *quitAfter, shot: *shot}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	a.output = filepath.Join(abs, session.Stamp("hwcamera"))
	a.scanDevices(*camID, *micID)
	defer a.closeDevices()

	ebiten.SetWindowTitle("hwcamera")
	ebiten.SetWindowSize(1280, 760)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	if err := ebiten.RunGame(a); err != nil && !errors.Is(err, ebiten.Termination) {
		log.Fatal(err)
	}
}

// scanDevices (re)lists the cameras and microphones, keeping the current
// selections where they still exist.
func (a *app) scanDevices(camID, micID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if camID == "" && a.camIdx < len(a.cams) {
		camID = a.cams[a.camIdx].ID
	}
	if micID == "" && a.micIdx > 0 && a.micIdx <= len(a.mics) {
		micID = a.mics[a.micIdx-1].ID
	}
	cams, err := hwcamera.Devices(ctx)
	if err != nil {
		a.status = "cameras: " + err.Error()
	}
	a.cams = cams
	a.camNames = nil
	a.camIdx = -1
	for i, d := range cams {
		name := d.Name
		if d.Virtual {
			name += " (virtual)"
		}
		a.camNames = append(a.camNames, name)
		if d.ID == camID {
			a.camIdx = i
		}
	}
	if a.camIdx < 0 {
		a.camIdx = defaultCamera(cams)
	}
	if len(cams) == 0 {
		a.camNames = []string{"(no camera)"}
	}

	mics, err := audio.Devices(ctx)
	if err != nil {
		a.status = "microphones: " + err.Error()
	}
	a.mics = nil
	a.micNames = []string{"(none)"}
	a.micIdx = 0
	for _, d := range mics {
		if d.Monitor {
			continue
		}
		a.mics = append(a.mics, d)
		a.micNames = append(a.micNames, d.Name)
		if d.ID == micID || (micID == "" && d.Default) {
			a.micIdx = len(a.mics)
		}
	}
	if a.micIdx == 0 && len(a.mics) > 0 {
		a.micIdx = 1
	}
	a.buildModes()
	a.openCam, a.openMode, a.openMic = -1, -1, -1 // reopen
}

// defaultCamera is the camera Open("") picks: the first physical one.
func defaultCamera(cams []hwcamera.DeviceInfo) int {
	for i, d := range cams {
		if !d.Virtual {
			return i
		}
	}
	return 0
}

// buildModes fills the mode menu for the selected camera: every size,
// largest first, at each frame rate of 15 fps or more.
func (a *app) buildModes() {
	var cur mode
	if a.modeIdx < len(a.modes) {
		cur = a.modes[a.modeIdx]
	} else {
		cur = mode{session.Size{W: 1280, H: 720}, 30}
	}
	a.modes, a.modeKeys = nil, nil
	if a.camIdx < len(a.cams) {
		for _, m := range a.cams[a.camIdx].Modes {
			rates := m.FrameRates
			if len(rates) == 0 {
				rates = []float64{30}
			}
			for _, r := range rates {
				if r < 15 && r != rates[0] {
					continue
				}
				md := mode{session.Size{W: m.Width, H: m.Height}, r}
				if !slices.Contains(a.modes, md) {
					a.modes = append(a.modes, md)
				}
			}
		}
	}
	slices.SortStableFunc(a.modes, func(x, y mode) int {
		if d := y.size.W*y.size.H - x.size.W*x.size.H; d != 0 {
			return d
		}
		return int(y.fps - x.fps)
	})
	if len(a.modes) == 0 {
		a.modes = []mode{{session.Size{W: 1280, H: 720}, 30}}
	}
	a.modeIdx = 0
	best := -1
	for i, m := range a.modes {
		a.modeKeys = append(a.modeKeys, fmt.Sprintf("%dx%d @%g fps", m.size.W, m.size.H, m.fps))
		// Keep the current mode, else the one closest to it.
		if m == cur {
			a.modeIdx, best = i, 0
		} else if best != 0 {
			d := abs(m.size.W*m.size.H-cur.size.W*cur.size.H)*100 + int(abs(int(m.fps-cur.fps)))
			if best < 0 || d < best {
				a.modeIdx, best = i, d
			}
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// settings are the recording settings of the current selections.
func (a *app) settings() session.Settings {
	s := session.Defaults()
	s.Media = mediaChoices[a.mediaIdx]
	if a.camIdx < len(a.cams) && a.camIdx != defaultCamera(a.cams) {
		s.Camera = a.cams[a.camIdx].ID
	}
	m := a.modes[a.modeIdx]
	s.Size, s.FPS = m.size, m.fps
	s.Codec = codecChoices[a.codecIdx]
	if a.micIdx > 0 {
		if d := a.mics[a.micIdx-1]; !d.Default {
			s.Mic = d.ID
		}
	}
	if a.mic != nil {
		s.Rate, s.Channels = a.mic.m.SampleRate(), a.mic.m.Channels()
	}
	s.Output = a.output
	return s
}

// syncDevices reopens the camera or the microphone after a selection changed.
func (a *app) syncDevices() {
	if a.camIdx != a.openCam {
		a.buildModes()
	}
	if a.camIdx != a.openCam || a.modeIdx != a.openMode {
		a.openCam, a.openMode = a.camIdx, a.modeIdx
		if a.cam != nil {
			a.cam.stop()
			a.cam = nil
		}
		if a.camIdx < len(a.cams) {
			s := a.settings()
			s.Camera = a.cams[a.camIdx].ID
			c, err := startCapture(s)
			if err != nil {
				a.status = fmt.Sprintf("%s: %v", a.cams[a.camIdx].Name, err)
				if errors.Is(err, hwcamera.ErrPermission) {
					a.status += "\n" + session.Hint("this app (or the terminal running it)")
				}
			} else {
				a.cam = c
				a.mirror = a.cams[a.camIdx].Position == hwcamera.PositionFront
				w, h := c.cam.Size()
				if a.img == nil || a.img.Bounds().Dx() != w || a.img.Bounds().Dy() != h {
					a.img = ebiten.NewImage(w, h)
				}
				a.last = nil
			}
		}
	}
	if a.micIdx != a.openMic {
		a.openMic = a.micIdx
		if a.mic != nil {
			a.mic.stop()
			a.mic = nil
		}
		if a.micIdx > 0 {
			s := session.Defaults()
			s.Mic = a.mics[a.micIdx-1].ID
			m, err := startMic(s)
			if err != nil {
				a.status = fmt.Sprintf("%s: %v", a.mics[a.micIdx-1].Name, err)
			} else {
				a.mic = m
			}
		}
	}
}

func (a *app) closeDevices() {
	if a.rec != nil {
		a.stopRecording()
	}
	if a.cam != nil {
		a.cam.stop()
	}
	if a.mic != nil {
		a.mic.stop()
	}
}

func (a *app) startRecording() {
	s := a.settings()
	if s.Media&session.Video != 0 && a.cam == nil {
		a.status = "no camera open"
		return
	}
	if s.Media&session.Audio != 0 && a.mic == nil {
		a.status = "no microphone open"
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.Output), 0o755); err != nil {
		a.status = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cam *hwcamera.Camera
	if a.cam != nil {
		cam = a.cam.cam
	}
	var mic *audio.Mic
	if a.mic != nil {
		mic = a.mic.m
	}
	r, err := session.StartRecording(ctx, s, cam, mic)
	if err != nil {
		a.status = err.Error()
		return
	}
	if s.Media&session.Video != 0 {
		a.cam.setRecorder(r)
	}
	if s.Media&session.Audio != 0 {
		a.mic.setWriter(r)
	}
	a.rec, a.recStart = r, time.Now()
	a.status = "recording " + baseNames(r.Files())
}

func (a *app) stopRecording() {
	r := a.rec
	if r == nil {
		return
	}
	var errs []string
	// Detach the recording from both devices before closing it: no write
	// can come after Close.
	if a.cam != nil {
		if _, err := a.cam.setRecorder(nil); err != nil {
			errs = append(errs, "video: "+err.Error())
		}
	}
	if a.mic != nil {
		if _, err := a.mic.setWriter(nil); err != nil {
			errs = append(errs, "audio: "+err.Error())
		}
	}
	if err := r.Close(); err != nil {
		errs = append(errs, err.Error())
	}
	a.rec = nil
	if len(errs) > 0 {
		a.status = strings.Join(errs, "\n")
	} else {
		a.status = fmt.Sprintf("saved %s (%s) in %s", baseNames(r.Files()), time.Since(a.recStart).Truncate(100*time.Millisecond), filepath.Dir(r.Settings.Output))
	}
	// The next recording gets a new name in the same folder.
	a.output = filepath.Join(filepath.Dir(a.output), session.Stamp("hwcamera"))
}

func (a *app) snapshot() {
	if a.last == nil {
		a.status = "no frame yet"
		return
	}
	path := filepath.Join(filepath.Dir(a.output), session.Stamp("hwcamera")+".png")
	f, err := os.Create(path)
	if err == nil {
		err = png.Encode(f, a.last)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		a.status = err.Error()
		return
	}
	a.snapPath = path
	a.status = "saved " + filepath.Base(path) + " in " + filepath.Dir(path)
}

func (a *app) Update() error {
	if a.began.IsZero() {
		a.began = time.Now()
	}
	if a.quitAfter > 0 && time.Since(a.began) > a.quitAfter && (a.shot == "" || a.shotDone) {
		return ebiten.Termination
	}
	a.scale = ebiten.Monitor().DeviceScaleFactor()
	if a.rec != nil {
		// A device that fails mid-recording ends it.
		if (a.cam != nil && a.cam.recErr() != nil) || (a.mic != nil && a.mic.writeErr() != nil) {
			a.stopRecording()
		}
	}
	a.ui.Update(func(ctx *debugui.Context) error {
		a.panel(ctx)
		return nil
	})
	if a.rec == nil {
		a.syncDevices()
	}
	if a.cam != nil {
		if f := a.cam.take(); f != nil {
			if f.Strides[0] == 4*f.Width && a.img.Bounds().Dx() == f.Width && a.img.Bounds().Dy() == f.Height {
				a.img.WritePixels(f.Planes[0])
				if a.last == nil {
					a.last = image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
				}
				copy(a.last.Pix, f.Planes[0])
			}
			f.Release()
		}
	}
	return nil
}

func (a *app) Draw(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	vw := sw - int(panelWidth*a.scale)
	if a.img != nil && a.cam != nil && vw > 0 {
		iw, ih := a.img.Bounds().Dx(), a.img.Bounds().Dy()
		k := min(float64(vw)/float64(iw), float64(sh)/float64(ih))
		op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
		if a.mirror {
			op.GeoM.Scale(-1, 1)
			op.GeoM.Translate(float64(iw), 0)
		}
		op.GeoM.Scale(k, k)
		op.GeoM.Translate((float64(vw)-float64(iw)*k)/2, (float64(sh)-float64(ih)*k)/2)
		screen.DrawImage(a.img, op)
	}
	a.ui.Draw(screen)
	if a.shot != "" && !a.shotDone && a.quitAfter > 0 && time.Since(a.began) > a.quitAfter {
		a.shotDone = true
		img := image.NewRGBA(screen.Bounds())
		screen.ReadPixels(img.Pix)
		if f, err := os.Create(a.shot); err == nil {
			png.Encode(f, img)
			f.Close()
		}
	}
}

// Layout works in device pixels so the preview is sharp on HiDPI screens;
// the panel is scaled up to match.
func (a *app) Layout(ow, oh int) (int, int) {
	s := a.scale
	if s == 0 {
		s = 1
	}
	return int(float64(ow) * s), int(float64(oh) * s)
}

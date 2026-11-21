package main

import (
	"fmt"
	"image"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ebitengine/debugui"
	"github.com/hajimehoshi/ebiten/v2"

	"github.com/shibukawa/hwcamera/cmd/internal/session"
)

// panel lays out the control panel on the right of the window.
func (a *app) panel(ctx *debugui.Context) {
	scale := max(1, int(math.Round(a.scale)))
	ctx.SetScale(scale)
	w, h := ebiten.WindowSize()
	x := max(0, w-panelWidth)
	ctx.Window("hwcamera", image.Rect(x, 0, w, h), func(layout debugui.ContainerLayout) {
		ctx.Header("Camera", true, func() {
			ctx.SetGridLayout([]int{60, -1}, nil)
			ctx.Text("Device")
			a.lock(ctx, func() { ctx.Dropdown(&a.camIdx, a.camNames) })
			ctx.Text("Mode")
			a.lock(ctx, func() { ctx.Dropdown(&a.modeIdx, a.modeKeys) })
			ctx.Text("")
			ctx.Checkbox(&a.mirror, "Mirror preview")
			ctx.SetGridLayout([]int{-1}, nil)
			if a.cam != nil {
				fps, _, err := a.cam.stats()
				ctx.Text(fmt.Sprintf("%s -> %s, %.1f fps", a.cam.cam.Mode(), a.cam.cam.Format(), fps))
				if err != nil {
					ctx.Text("error: " + err.Error())
				}
			}
		})
		ctx.Header("Microphone", true, func() {
			ctx.SetGridLayout([]int{60, -1}, nil)
			ctx.Text("Device")
			a.lock(ctx, func() { ctx.Dropdown(&a.micIdx, a.micNames) })
			if a.mic != nil {
				ctx.Text("Level")
				ctx.Text(meter(a.mic.Level()))
				ctx.SetGridLayout([]int{-1}, nil)
				ctx.Text(fmt.Sprintf("%d Hz, %d ch (%s)", a.mic.m.SampleRate(), a.mic.m.Channels(), a.mic.m.Info().Backend))
				if err := a.mic.readErr(); err != nil {
					ctx.Text("error: " + err.Error())
				}
			}
			ctx.SetGridLayout([]int{-1}, nil)
			a.lock(ctx, func() {
				ctx.Button("Rescan devices").On(func() { a.scanDevices("", "") })
			})
		})
		ctx.Header("Record", true, func() {
			ctx.SetGridLayout([]int{60, -1}, nil)
			ctx.Text("Record")
			a.lock(ctx, func() { ctx.Dropdown(&a.mediaIdx, mediaNames) })
			ctx.Text("Codec")
			a.lock(ctx, func() { ctx.Dropdown(&a.codecIdx, codecNames) })
			ctx.Text("Save as")
			a.lock(ctx, func() { ctx.TextField(&a.output) })
			ctx.SetGridLayout([]int{-1}, nil)
			ctx.Text("-> " + baseNames(a.settings().Files()))
			ctx.SetGridLayout([]int{-1, -1}, []int{28})
			if a.rec != nil {
				ctx.Button(fmt.Sprintf("Stop  %s", time.Since(a.recStart).Truncate(time.Second))).On(a.stopRecording)
			} else {
				ctx.Button("Start recording").On(a.startRecording)
			}
			ctx.Button("Snapshot (PNG)").On(a.snapshot)
			ctx.SetGridLayout([]int{-1}, nil)
			if r := a.rec; r != nil {
				var parts []string
				if r.Settings.Media&session.Video != 0 {
					parts = append(parts, fmt.Sprintf("%d frames", r.Frames()))
				}
				if r.Settings.Media&session.Audio != 0 {
					parts = append(parts, fmt.Sprintf("audio %.1fs", r.AudioDuration().Seconds()))
				}
				ctx.Text(strings.Join(parts, ", "))
			}
			if a.status != "" {
				ctx.Text(a.status)
			}
		})
		ctx.Header("Command line (hwcamera-cli)", true, func() {
			ctx.SetGridLayout([]int{-1}, nil)
			s := a.settings()
			if a.rec != nil {
				s = a.rec.Settings
			}
			rec := session.Command(s.RecArgs())
			ctx.Text("Record (Ctrl-C stops; add -duration 30s for a fixed length):")
			ctx.Text(rec)
			ctx.Button("Copy record command").On(func() { a.copy(rec) })
			snapOut := a.snapPath
			if snapOut == "" {
				snapOut = "frame.png"
			}
			snap := session.Command(session.SnapArgs(s.Camera, s.Size, snapOut))
			ctx.Text("Snapshot:")
			ctx.Text(snap)
			ctx.Button("Copy snapshot command").On(func() { a.copy(snap) })
			ctx.Text("Install: go install github.com/shibukawa/hwcamera/cmd/hwcamera-cli@latest")
		})
	})
}

// lock runs f, then undoes any change of the device settings while a
// recording runs: they cannot change mid-file.
func (a *app) lock(ctx *debugui.Context, f func()) {
	if a.rec == nil {
		f()
		return
	}
	cam, mode, mic, media, codec, out := a.camIdx, a.modeIdx, a.micIdx, a.mediaIdx, a.codecIdx, a.output
	f()
	if cam != a.camIdx || mode != a.modeIdx || mic != a.micIdx || media != a.mediaIdx || codec != a.codecIdx || out != a.output {
		a.camIdx, a.modeIdx, a.micIdx, a.mediaIdx, a.codecIdx, a.output = cam, mode, mic, media, codec, out
		a.status = "stop the recording to change settings"
	}
}

func meter(rms float64) string {
	// -90 dBFS .. 0 dBFS over 30 bars: a quiet room is around -70.
	db := -90.0
	if rms > 0 {
		db = max(-90, 20*math.Log10(rms))
	}
	n := int((db + 90) / 3)
	return fmt.Sprintf("%-30s %3.0f dB", strings.Repeat("|", n), db)
}

// baseNames lists the file names of paths in one folder.
func baseNames(paths []string) string {
	var names []string
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	return strings.Join(names, " + ")
}

// copy puts s on the clipboard with the system's command-line tool.
func (a *app) copy(s string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip")
	default:
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		}
	}
	cmd.Stdin = strings.NewReader(s)
	if err := cmd.Run(); err != nil {
		a.status = "copy failed (" + err.Error() + "); the command is also printed to the terminal"
	} else {
		a.status = "copied to the clipboard"
	}
	fmt.Println(s)
}

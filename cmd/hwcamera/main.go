// Command hwcamera lists cameras, takes snapshots and checks capture:
//
//	hwcamera list                         # cameras and their modes
//	hwcamera snap -o frame.png            # one frame as PNG (or .jpg)
//	hwcamera check                        # capture from every camera, report a Markdown table
//	hwcamera mics                         # audio inputs
//	hwcamera rec -o rec.wav -seconds 5    # record the default microphone
//
// Set HWCAMERA_BACKEND to restrict the backends (v4l2, avfoundation,
// mediafoundation).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shibukawa/hwcamera"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "list":
		err = list(ctx, os.Args[2:])
	case "snap":
		err = snap(ctx, os.Args[2:])
	case "check":
		err = check(ctx, os.Args[2:])
	case "mics":
		err = mics(ctx, os.Args[2:])
	case "rec":
		err = rec(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hwcamera:", err)
		if errors.Is(err, hwcamera.ErrPermission) {
			fmt.Fprintln(os.Stderr, hint())
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hwcamera list | snap [-device ID] [-size WxH] [-o file.png] | check [-frames N] | mics | rec [-device ID] [-o file.wav] [-seconds N]")
	os.Exit(2)
}

func hint() string {
	switch runtime.GOOS {
	case "linux":
		return "hint: add yourself to the video group (sudo usermod -aG video $USER) and log in again"
	case "darwin":
		return "hint: allow the terminal app in System Settings > Privacy & Security > Camera"
	case "windows":
		return "hint: turn on Settings > Privacy & security > Camera > Let desktop apps access your camera"
	}
	return ""
}

func list(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Parse(args)
	ds, err := hwcamera.Devices(ctx)
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("no cameras (backends:", strings.Join(hwcamera.Backends(), ", ")+")")
		return nil
	}
	for _, d := range ds {
		fmt.Printf("%s  \"%s\"  [%s, %s]\n", d.ID, d.Name, d.Backend, d.Position)
		for _, m := range d.Modes {
			fmt.Println("    ", m)
		}
	}
	return nil
}

func parseSize(s string) (int, int, error) {
	if s == "" {
		return 0, 0, nil
	}
	var w, h int
	if _, err := fmt.Sscanf(s, "%dx%d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("bad size %q", s)
	}
	return w, h, nil
}

func snap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snap", flag.ExitOnError)
	device := fs.String("device", "", "camera ID (see list)")
	size := fs.String("size", "", "WxH (default 1280x720 or nearest)")
	out := fs.String("o", "frame.png", "output .png or .jpg")
	skip := fs.Int("skip", 10, "frames to drop first while exposure settles")
	native := fs.String("native", "", "force the native format: mjpeg, yuyv, nv12")
	fs.Parse(args)
	w, h, err := parseSize(*size)
	if err != nil {
		return err
	}
	opts := []hwcamera.Option{hwcamera.WithSize(w, h), hwcamera.WithFormat(hwcamera.RGBA)}
	if *native != "" {
		f, err := parseFormat(*native)
		if err != nil {
			return err
		}
		opts = append(opts, hwcamera.WithNativeFormat(f))
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cam, err := hwcamera.Open(ctx, *device, opts...)
	if err != nil {
		return err
	}
	defer cam.Close()
	var f *hwcamera.Frame
	for i := 0; i <= *skip; i++ {
		if f != nil {
			f.Release()
		}
		if f, err = cam.Read(ctx); err != nil {
			return err
		}
	}
	defer f.Release()
	img := &image.RGBA{Pix: f.Planes[0], Stride: f.Strides[0], Rect: image.Rect(0, 0, f.Width, f.Height)}
	file, err := os.Create(*out)
	if err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(*out)) {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(file, img, &jpeg.Options{Quality: 92})
	default:
		err = png.Encode(file, img)
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %dx%d from \"%s\" (%s, native %s)\n", *out, f.Width, f.Height, cam.Info().Name, cam.Info().Backend, cam.Mode())
	return nil
}

func parseFormat(s string) (hwcamera.PixelFormat, error) {
	switch strings.ToLower(s) {
	case "mjpeg", "mjpg", "jpeg":
		return hwcamera.MJPEG, nil
	case "yuyv", "yuy2":
		return hwcamera.YUYV, nil
	case "uyvy":
		return hwcamera.UYVY, nil
	case "nv12":
		return hwcamera.NV12, nil
	case "i420", "yu12":
		return hwcamera.I420, nil
	case "bgra":
		return hwcamera.BGRA, nil
	case "rgba":
		return hwcamera.RGBA, nil
	}
	return 0, fmt.Errorf("unknown format %q", s)
}

func check(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	frames := fs.Int("frames", 60, "frames to capture per test")
	fs.Parse(args)
	fmt.Printf("## hwcamera check\n\n- OS: %s/%s\n- Backends: %s\n\n", runtime.GOOS, runtime.GOARCH, strings.Join(hwcamera.Backends(), ", "))
	ds, err := hwcamera.Devices(ctx)
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("No cameras found.")
		return nil
	}
	fmt.Println("| Camera | Backend | Request | Native mode | Frames | FPS | Dropped | First frame | Result |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, d := range ds {
		tests := []struct {
			name string
			opts []hwcamera.Option
		}{
			{"default NV12", nil},
			{"RGBA", []hwcamera.Option{hwcamera.WithFormat(hwcamera.RGBA)}},
		}
		// The largest mode, natively, to exercise MJPEG where the camera has it.
		if len(d.Modes) > 0 {
			best := d.Modes[0]
			for _, m := range d.Modes {
				if m.Width*m.Height > best.Width*best.Height {
					best = m
				}
			}
			tests = append(tests, struct {
				name string
				opts []hwcamera.Option
			}{fmt.Sprintf("max %s %dx%d", best.Format, best.Width, best.Height),
				[]hwcamera.Option{hwcamera.WithSize(best.Width, best.Height), hwcamera.WithNativeFormat(best.Format)}})
		}
		for _, t := range tests {
			row := runCheck(ctx, d, t.opts, *frames)
			fmt.Printf("| %s | %s | %s | %s |\n", d.Name, d.Backend, t.name, row)
		}
	}
	return nil
}

func runCheck(ctx context.Context, d hwcamera.DeviceInfo, opts []hwcamera.Option, n int) string {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(n)*200*time.Millisecond+5*time.Second)
	defer cancel()
	t0 := time.Now()
	cam, err := hwcamera.Open(ctx, d.ID, opts...)
	if err != nil {
		return fmt.Sprintf("- | - | - | - | - | open: %v", err)
	}
	defer cam.Close()
	var first time.Duration
	var start time.Time
	var firstSeq, lastSeq uint64
	got := 0
	for got < n {
		f, err := cam.Read(ctx)
		if err != nil {
			return fmt.Sprintf("%s | %d | - | - | - | read: %v", cam.Mode(), got, err)
		}
		if got == 0 {
			first = time.Since(t0)
			start = time.Now()
			firstSeq = f.Sequence
		}
		lastSeq = f.Sequence
		if err := validate(f, cam.Format()); err != nil {
			f.Release()
			return fmt.Sprintf("%s | %d | - | - | - | bad frame: %v", cam.Mode(), got, err)
		}
		f.Release()
		got++
	}
	el := time.Since(start)
	fps := float64(got-1) / el.Seconds()
	dropped := int64(lastSeq-firstSeq) - int64(got-1)
	if dropped < 0 {
		dropped = 0
	}
	return fmt.Sprintf("%s | %d | %.1f | %d | %s | ok", cam.Mode(), got, fps, dropped, first.Truncate(time.Millisecond))
}

func validate(f *hwcamera.Frame, want hwcamera.PixelFormat) error {
	if f.Format != want {
		return fmt.Errorf("format %s, want %s", f.Format, want)
	}
	if f.Width <= 0 || f.Height <= 0 || len(f.Planes) == 0 {
		return fmt.Errorf("empty frame")
	}
	if f.Format != hwcamera.MJPEG && len(f.Planes[0]) < f.Strides[0]*(f.Height-1)+f.Width {
		return fmt.Errorf("plane 0 is %d bytes for %dx%d stride %d", len(f.Planes[0]), f.Width, f.Height, f.Strides[0])
	}
	return nil
}

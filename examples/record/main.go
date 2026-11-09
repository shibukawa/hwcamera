// Command record encodes the camera into an MP4 file with the hardware
// encoder (hwmediacodec):
//
//	go run ./record -o camera.mp4 -duration 10s
//	go run ./record -codec hevc -size 1920x1080 -fps 30 -bitrate 8000000
//
// Ctrl-C ends the recording early; the file is finished either way.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/mediacodec"
	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

func main() {
	out := flag.String("o", "camera.mp4", "output file")
	device := flag.String("device", "", "camera ID (hwcamera list) or a kind (front, back, back+ultrawide, ...); empty for the default camera")
	codecName := flag.String("codec", "h264", "h264 or hevc")
	size := flag.String("size", "1280x720", "capture size")
	fps := flag.Float64("fps", 30, "frame rate")
	bitrate := flag.Int("bitrate", 4_000_000, "bits per second")
	duration := flag.Duration("duration", 10*time.Second, "length, e.g. 10s or 2m (0 = until Ctrl-C)")
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil {
		log.Fatalf("bad -size %q", *size)
	}
	codec := hwmediacodec.H264
	if strings.EqualFold(*codecName, "hevc") {
		codec = hwmediacodec.HEVC
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	id, opts := *device, []hwcamera.Option{hwcamera.WithSize(w, h), hwcamera.WithFrameRate(*fps)}
	if k, err := hwcamera.ParseKind(id); err == nil { // -device back, front+physical, ...
		id = ""
		opts = append(opts, hwcamera.WithKind(k))
	}
	cam, err := hwcamera.Open(ctx, id, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer cam.Close()
	fmt.Printf("%s: %s (%s), delivering %s\n", cam.Info().Backend, cam.Info().Name, cam.Mode(), cam.Format())

	enc, err := mediacodec.NewEncoder(ctx, cam, codec, hwmediacodec.WithBitrate(*bitrate), hwmediacodec.WithLowLatency())
	if err != nil {
		log.Fatal(err)
	}
	defer enc.Close()
	file, err := mp4.CreateVideoFile(*out, codec, mediacodec.TimeScale)
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	n, err := mediacodec.Record(ctx, cam, enc, file)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Fatal(err)
	}
	el := time.Since(start)
	fmt.Printf("%s: %d frames in %s (%.1f fps)\n", *out, n, el.Truncate(time.Millisecond), float64(n)/el.Seconds())
}

// Command record encodes the camera into an MP4 file with the hardware
// encoder (hwmediacodec):
//
//	go run ./record -o camera.mp4 -seconds 10
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
	device := flag.String("device", "", "camera ID (hwcamera list); empty for the first camera")
	codecName := flag.String("codec", "h264", "h264 or hevc")
	size := flag.String("size", "1280x720", "capture size")
	fps := flag.Float64("fps", 30, "frame rate")
	bitrate := flag.Int("bitrate", 4_000_000, "bits per second")
	seconds := flag.Float64("seconds", 10, "length (0 = until Ctrl-C)")
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
	if *seconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*seconds*float64(time.Second)))
		defer cancel()
	}

	cam, err := hwcamera.Open(ctx, *device, hwcamera.WithSize(w, h), hwcamera.WithFrameRate(*fps))
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

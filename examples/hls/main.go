// Command hls streams the camera to browsers as live HLS, encoded by the
// hardware encoder (hwmediacodec) and served from memory:
//
//	go run ./hls                     # http://127.0.0.1:8080/
//	go run ./hls -addr :8080         # watch from other machines
//
// Safari plays HLS natively, other browsers through hls.js. Expect a few
// seconds of latency; that is HLS. For sub-second latency use the WebRTC
// module of hwmediacodec (net/webrtc) with the same encoder.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/mediacodec"
	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
	"github.com/shibukawa/hwmediacodec/net/hls"
)

const page = `<!doctype html><meta charset="utf-8"><title>hwcamera</title>
<style>body{margin:0;background:#111;display:grid;place-items:center;height:100vh}video{max-width:100%;max-height:100vh}</style>
<video id="v" autoplay muted playsinline controls></video>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1.5.17/dist/hls.min.js"></script>
<script>
const v = document.getElementById("v");
async function start() {
  // The playlist answers 503 until the first segment is cut.
  while (!(await fetch("index.m3u8", {method: "HEAD"})).ok) await new Promise(r => setTimeout(r, 500));
  if (v.canPlayType("application/vnd.apple.mpegurl")) { v.src = "index.m3u8"; return; }
  const h = new Hls({liveSyncDurationCount: 2}); h.loadSource("index.m3u8"); h.attachMedia(v);
}
start();
</script>`

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	device := flag.String("device", "", "camera ID (hwcamera list) or a kind (front, back, back+ultrawide, ...); empty for the default camera")
	codecName := flag.String("codec", "h264", "h264 or hevc (HEVC plays in Safari only)")
	size := flag.String("size", "1280x720", "capture size")
	fps := flag.Float64("fps", 30, "frame rate")
	bitrate := flag.Int("bitrate", 3_000_000, "bits per second")
	segment := flag.Duration("segment", time.Second, "segment length")
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
	rate := cam.Mode().MaxFrameRate()
	if rate == 0 {
		rate = *fps
	}
	// A keyframe at every segment boundary.
	enc, err := mediacodec.NewEncoder(ctx, cam, codec,
		hwmediacodec.WithBitrate(*bitrate),
		hwmediacodec.WithLowLatency(),
		hwmediacodec.WithKeyframeInterval(int(rate*segment.Seconds()+0.5)))
	if err != nil {
		log.Fatal(err)
	}
	defer enc.Close()

	playlist := hls.NewPlaylist(6, *segment)
	seg, err := mp4.NewSegmenter(codec, mediacodec.TimeScale, *segment, playlist.SetInit, playlist.Add)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	mux.Handle("/", playlist)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	fmt.Printf("%s: %s (%s) -> http://%s/\n", cam.Info().Backend, cam.Info().Name, cam.Mode(), *addr)

	n, err := mediacodec.Record(ctx, cam, enc, seg)
	seg.Close()
	playlist.End()
	srv.Close()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n, "frames streamed")
}

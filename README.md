<img src="assets/icon.svg" width="64" height="64" alt="">

# hwcamera

Camera capture from Go, without cgo, through each operating system's own
camera API.

| Platform | Backend | How it is reached |
| --- | --- | --- |
| Linux (64-bit) | V4L2 | `ioctl` and `mmap` on `/dev/videoN` through `golang.org/x/sys/unix`; no library at all |
| macOS | AVFoundation | the Objective-C runtime through [purego](https://github.com/ebitengine/purego) (`AVCaptureSession`, a runtime-registered sample buffer delegate) |
| Windows x64 / ARM64 | Media Foundation | `IMFSourceReader` through raw COM vtable calls and `golang.org/x/sys/windows` |

`CGO_ENABLED=0 go build` works everywhere and the module cross-compiles from
one machine, like its siblings
[hwmediacodec](https://github.com/shibukawa/hwmediacodec) (hardware video
codecs) and [hwnpu](https://github.com/shibukawa/hwnpu) (NPUs). hwcamera is
the front of that chain: frames come out as NV12, the input format of every
hardware encoder, and the `tensor` package turns them into model inputs for
an NPU.

## Packages

| Import path | What it is |
| --- | --- |
| `github.com/shibukawa/hwcamera` | `Devices`, `Open`, `Camera.Read`, frames; format conversion in Go (NV12, I420, YUYV, UYVY, RGBA, BGRA; MJPEG decoding) |
| `.../audio` | Microphone capture: `Devices`, `Open`, `Mic.Read` returning PCM (S16 or F32, any rate and channel count; the OS converts) |
| `.../tensor` | Frame → input tensor: stretch / letterbox / centre crop, RGB / BGR / gray, NCHW / NHWC, float32 / float16 / uint8 / int8, mean and std; maps model coordinates back to the frame |
| `.../mediacodec` | Separate module: the camera as a `hwmediacodec.FrameReader`, an encoder sized for the camera, `Record` (camera → encoder → any `PacketWriter`: MP4, HLS, WebRTC) |
| `.../examples` | Separate module: `preview` (camera in an Ebitengine window), `record` (MP4), `hls` (live stream to browsers), `npu` (image model on the NPU over camera frames) |
| `.../cmd/hwcamera` | `list`, `snap`, `check` |

The core module depends only on purego and `golang.org/x/sys`.

## Status

| Platform | State |
| --- | --- |
| Linux, V4L2 | Verified with the kernel's `vivid` virtual camera on Linux 7.0 (amd64): enumeration, NV12 / I420 / YUYV / UYVY / RGB capture at 25 fps without drops up to 3840x2160, conversion of every format to the same colours, camera → VA-API H.264 / HEVC (Radeon 880M) → MP4 and live HLS. Structure layouts and ioctl numbers checked against the kernel headers. **Not yet verified with a real USB camera (MJPEG).** |
| macOS, AVFoundation | Builds (`CGO_ENABLED=0`, arm64 and amd64). **Not yet run on a Mac.** |
| Windows, Media Foundation | Builds (`CGO_ENABLED=0`, amd64 and arm64). **Not yet run on Windows.** |

Please run the check on your machine and report the output in an issue:

```sh
go run github.com/shibukawa/hwcamera/cmd/hwcamera@latest check
```

## Usage

```go
cam, err := hwcamera.Open(ctx, "", // "" = first camera; IDs from hwcamera.Devices
	hwcamera.WithSize(1280, 720),
	hwcamera.WithFrameRate(30))
if err != nil { ... }
defer cam.Close()

for {
	f, err := cam.Read(ctx)
	if err != nil { ... }
	// f.Planes[0] = Y, f.Planes[1] = interleaved CbCr (NV12), with f.Strides
	f.Release() // hand the buffer back to the driver
}
```

- With an empty ID, `Open` takes the system's default camera (macOS:
  the one chosen in Control Center), then built-in cameras, then other
  physical ones. Virtual cameras (`DeviceInfo.Virtual`: OBS, VR and
  conferencing drivers, v4l2loopback) are picked only when no physical
  camera exists; open them by ID. `Devices` returns the same order.
- The device's closest mode is used (`cam.Mode()`); frames are never
  scaled. Without `WithSize` the camera runs at 1280x720 where it can.
- `WithFormat` selects the frame format: `NV12` (default), `I420`, `YUYV`,
  `UYVY`, `RGBA`, `BGRA`, or `Any` for whatever the device delivers (no
  conversion; may be `MJPEG`).
- The library picks the device's native format by cost: the requested one,
  then other raw formats, MJPEG last. A USB 2 camera offers 720p30 and
  1080p30 only as MJPEG, and then MJPEG is used and decoded in Go (also
  the frames without Huffman tables that many cameras send).
  `WithNativeFormat` forces a format.
- Frames in the native format may live in driver memory (V4L2 `mmap`):
  call `Release` soon, the driver stalls when all its buffers (4, see
  `WithBuffers`) are held. Converted frames live in Go memory.
- `Frame.Timestamp` counts from the first frame; `Frame.Sequence` gaps are
  frames the device dropped.
- `HWCAMERA_BACKEND=v4l2` (comma-separated) restricts the backends.

### Microphones

```go
mic, err := audio.Open(ctx, "", // "" = the default input; IDs from audio.Devices
	audio.WithSampleRate(48000), audio.WithChannels(1)) // the defaults
if err != nil { ... }
defer mic.Close()
for {
	b, err := mic.Read(ctx) // 20 ms per buffer (WithPeriod)
	if err != nil { ... }
	samples := b.S16() // or b.F32() with WithFormat(audio.F32)
	_ = samples
}
```

| Platform | Backend | State |
| --- | --- | --- |
| Linux | PulseAudio client library (`libpulse`, purego); works with PulseAudio and PipeWire | Verified on Linux 7.0 with pipewire-pulse: a 1 kHz tone played into a virtual sink comes back from its monitor within 1 LSB of the input, also resampled to 16 kHz stereo; no drops |
| macOS | Core Audio HAL (devices) and AudioQueue (capture), purego | Builds; **not yet run on a Mac** |
| Windows | WASAPI shared mode with automatic format conversion, COM vtables | Builds; **not yet run on Windows** |

- The OS audio stack resamples and mixes channels, so buffers always have the
  format asked for.
- About one second of buffers is kept when the reader falls behind; the next
  buffer then has `Discontinuity` set.
- `b.Level()` gives peak and RMS for a level meter; `hwcamera mics` lists the
  inputs and `hwcamera rec -o rec.wav` records one with a meter.
- On Linux the monitors of the outputs are listed too (`Monitor`):
  recording one records what the speakers play.
- macOS asks for microphone permission (the terminal app's, for a
  command-line program; `NSMicrophoneUsageDescription` in an app bundle).
  Windows needs Settings › Privacy & security › Microphone › "Let desktop
  apps access your microphone".

### Into a hardware encoder

```go
import (
	"github.com/shibukawa/hwcamera/mediacodec"
	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

enc, _ := mediacodec.NewEncoder(ctx, cam, hwmediacodec.H264, hwmediacodec.WithBitrate(4_000_000))
defer enc.Close()
file, _ := mp4.CreateVideoFile("camera.mp4", hwmediacodec.H264, mediacodec.TimeScale)
n, err := mediacodec.Record(ctx, cam, enc, file) // until ctx is cancelled
file.Close()
```

Any `hwmediacodec.PacketWriter` works as the destination: an
`mp4.Segmenter` feeding `net/hls` for live HLS (see `examples/hls`), or the
WebRTC broadcaster of `hwmediacodec/net/webrtc`.

### Into a model on the NPU

```go
spec := tensor.Spec{Width: 224, Height: 224, Layout: tensor.NCHW, DType: tensor.Float16,
	Resize: tensor.CenterCrop, Mean: tensor.ImageNetMean, Std: tensor.ImageNetStd}
// or: spec, _ := tensor.FromShape(arg.Dims) and set DType / normalisation

in, _ := dev.AllocBuffer(spec.Bytes())        // hwnpu
tr, err := tensor.Fill(in.Bytes(), frame, spec) // no copy in between
// run the model, then map a detection back onto the frame:
fx, fy := tr.ToFrame(boxX, boxY)
```

`tensor` does not import hwnpu; any runtime that takes a byte buffer works.
Open the camera with `WithFormat(hwcamera.RGBA)` when the frames only feed
a model; that saves one conversion. See `examples/npu`.

## Platform notes

### Linux

- The user needs read and write access to `/dev/videoN` (the `video`
  group).
- Single-planar capture nodes only. UVC cameras (every USB and most laptop
  webcams) qualify. MIPI cameras behind an ISP that exposes only a
  media-controller pipeline (Intel IPU6, some Arm boards) need libcamera and
  are not listed.
- Only 64-bit kernels (`struct v4l2_buffer` differs on 32-bit ones).
- `ENOSPC` from `VIDIOC_STREAMON` means not enough USB bandwidth: choose
  MJPEG or a smaller size, or move a second camera to another bus.

### macOS

- The process needs camera permission. A command-line program inherits the
  permission of the terminal app (System Settings › Privacy & Security ›
  Camera); an app bundle needs `NSCameraUsageDescription` in its
  `Info.plist`. The first `Open` triggers the system prompt.
- Frames are delivered as NV12 or BGRA by AVFoundation itself, also from
  MJPEG cameras (`WithNativeFormat(MJPEG)` picks the device format, the
  frames still arrive decoded).

### Windows

- Settings › Privacy & security › Camera › "Let desktop apps access your
  camera" must be on (`ErrPermission` otherwise).
- Media Foundation decodes MJPEG and converts to NV12 inside the source
  reader.
- Listing the devices opens each camera briefly to read its modes, and
  `Open` lists the devices first, so opening one camera wakes every camera
  for a moment.

## Known limitations

- Colour: YUV frames are treated as BT.601 video range (what UVC cameras
  declare by default). hwmediacodec v0.5.1 cannot tag NV12 encoder input
  with a colour matrix, and decoders assume BT.709 for untagged HD
  streams, so recordings of HD sizes play back with slightly shifted
  colours (greens darker). A colour-matrix option for NV12 input in
  hwmediacodec would fix it.
- No camera controls yet (exposure, focus, white balance, zoom).
- No still-image capture at sensor resolution, no depth or IR streams.
- iOS, Android and browsers (`getUserMedia`) have no backend yet.

## License

Apache License 2.0. See [LICENSE](LICENSE).

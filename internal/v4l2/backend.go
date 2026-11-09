//go:build linux && (amd64 || arm64 || riscv64 || loong64 || ppc64le || ppc64 || s390x || mips64 || mips64le)

package v4l2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// Backend is the V4L2 backend.
type Backend struct{}

func (Backend) Name() string { return "v4l2" }

func (Backend) Available() bool { return true }

// formats maps V4L2 pixel formats to the library's. Formats outside this
// table (Bayer, 10-bit, H.264 from UVC 1.5 cameras) are not offered.
var formats = map[uint32]camera.PixelFormat{
	fourcc("NV12"): camera.NV12,
	fourcc("YU12"): camera.I420,
	fourcc("YUYV"): camera.YUYV,
	fourcc("UYVY"): camera.UYVY,
	fourcc("MJPG"): camera.MJPEG,
	fourcc("JPEG"): camera.MJPEG,
	fourcc("AR24"): camera.BGRA, // V4L2_PIX_FMT_ABGR32: B, G, R, A in memory
	fourcc("XR24"): camera.BGRA, // V4L2_PIX_FMT_XBGR32
	fourcc("BGR4"): camera.BGRA, // deprecated V4L2_PIX_FMT_BGR32
	fourcc("AB24"): camera.RGBA, // V4L2_PIX_FMT_RGBA32: R, G, B, A in memory
	fourcc("XB24"): camera.RGBA, // V4L2_PIX_FMT_RGBX32
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if e == unix.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

func openNode(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		switch {
		case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
			return -1, fmt.Errorf("%w: %s (add the user to the video group)", camera.ErrPermission, path)
		case errors.Is(err, unix.EBUSY):
			return -1, fmt.Errorf("%w: %s", camera.ErrBusy, path)
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENODEV):
			return -1, fmt.Errorf("%w: %s", camera.ErrNotFound, path)
		}
		return -1, &camera.BackendError{Backend: "v4l2", Op: "open " + path, Err: err}
	}
	return fd, nil
}

// nodes lists /dev/videoN in numeric order.
func nodes() []string {
	m, _ := filepath.Glob("/dev/video*")
	sort.Slice(m, func(i, j int) bool {
		if len(m[i]) != len(m[j]) {
			return len(m[i]) < len(m[j])
		}
		return m[i] < m[j]
	})
	return m
}

func (b Backend) Devices(ctx context.Context) ([]camera.DeviceInfo, error) {
	var out []camera.DeviceInfo
	var firstErr error
	for _, path := range nodes() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		info, err := probe(path)
		if err != nil {
			if firstErr == nil && !errors.Is(err, errNotCapture) {
				firstErr = err
			}
			continue
		}
		out = append(out, info)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

var errNotCapture = errors.New("not a video capture node")

func probe(path string) (camera.DeviceInfo, error) {
	fd, err := openNode(path)
	if err != nil {
		return camera.DeviceInfo{}, err
	}
	defer unix.Close(fd)
	var cp capability
	if err := ioctl(fd, vidiocQuerycap, unsafe.Pointer(&cp)); err != nil {
		return camera.DeviceInfo{}, errNotCapture
	}
	caps := cp.Capabilities
	if caps&capDeviceCaps != 0 {
		caps = cp.DeviceCaps
	}
	// UVC drivers expose a second node per camera for metadata; ISP
	// pipelines expose many. Only single-planar streaming capture nodes
	// that offer a format we understand count as cameras.
	if caps&capVideoCapture == 0 || caps&capStreaming == 0 {
		return camera.DeviceInfo{}, errNotCapture
	}
	modes := enumModes(fd)
	if len(modes) == 0 {
		return camera.DeviceInfo{}, errNotCapture
	}
	info := camera.DeviceInfo{
		ID:       path,
		Name:     cstr(cp.Card[:]),
		Backend:  "v4l2",
		Position: position(cstr(cp.BusInfo[:]), cstr(cp.Driver[:])),
		Modes:    modes,
	}
	return info, nil
}

func position(bus, driver string) camera.Position {
	switch {
	case strings.HasPrefix(bus, "usb-"):
		// Laptop webcams are USB too; nothing in V4L2 tells them apart
		// from a plugged-in one.
		return camera.PositionUnknown
	case strings.HasPrefix(bus, "platform:"):
		if driver == "vivid" || driver == "v4l2 loopback" || driver == "v4l2loopback" {
			return camera.PositionExternal
		}
		return camera.PositionFront
	}
	return camera.PositionUnknown
}

func enumModes(fd int) []camera.Mode {
	var modes []camera.Mode
	for i := uint32(0); ; i++ {
		fd0 := fmtdesc{Index: i, Type: bufTypeVideoCapture}
		if ioctl(fd, vidiocEnumFmt, unsafe.Pointer(&fd0)) != nil {
			break
		}
		pf, ok := formats[fd0.PixelFormat]
		if !ok {
			continue
		}
		for _, sz := range enumSizes(fd, fd0.PixelFormat) {
			modes = append(modes, camera.Mode{
				Format: pf, Width: sz[0], Height: sz[1],
				FrameRates: enumRates(fd, fd0.PixelFormat, sz[0], sz[1]),
			})
		}
	}
	modes = dedupe(modes)
	camera.SortModes(modes)
	return modes
}

// dedupe merges modes that differ only by V4L2 format code (MJPG and JPEG).
func dedupe(ms []camera.Mode) []camera.Mode {
	type key struct {
		f    camera.PixelFormat
		w, h int
	}
	idx := map[key]int{}
	var out []camera.Mode
	for _, m := range ms {
		k := key{m.Format, m.Width, m.Height}
		if i, ok := idx[k]; ok {
			out[i].FrameRates = mergeRates(out[i].FrameRates, m.FrameRates)
			continue
		}
		idx[k] = len(out)
		out = append(out, m)
	}
	return out
}

func mergeRates(a, b []float64) []float64 {
	for _, r := range b {
		dup := false
		for _, x := range a {
			if x == r {
				dup = true
			}
		}
		if !dup {
			a = append(a, r)
		}
	}
	return a
}

// commonSizes are offered from a stepwise or continuous range.
var commonSizes = [][2]int{
	{3840, 2160}, {2560, 1440}, {1920, 1080}, {1280, 960}, {1280, 720},
	{1024, 768}, {800, 600}, {640, 480}, {640, 360}, {320, 240},
}

func enumSizes(fd int, pf uint32) [][2]int {
	var out [][2]int
	for i := uint32(0); ; i++ {
		fs := frmsizeenum{Index: i, PixelFormat: pf}
		if ioctl(fd, vidiocEnumFramesizes, unsafe.Pointer(&fs)) != nil {
			break
		}
		switch fs.Type {
		case frmsizeDiscrete:
			out = append(out, [2]int{int(fs.U[0]), int(fs.U[1])})
		case frmsizeContinuous, frmsizeStepwise:
			minW, maxW, stepW := fs.U[0], fs.U[1], max(fs.U[2], 1)
			minH, maxH, stepH := fs.U[3], fs.U[4], max(fs.U[5], 1)
			for _, s := range commonSizes {
				w, h := uint32(s[0]), uint32(s[1])
				if w >= minW && w <= maxW && h >= minH && h <= maxH && (w-minW)%stepW == 0 && (h-minH)%stepH == 0 {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return out
}

func enumRates(fd int, pf uint32, w, h int) []float64 {
	var out []float64
	for i := uint32(0); ; i++ {
		fi := frmivalenum{Index: i, PixelFormat: pf, Width: uint32(w), Height: uint32(h)}
		if ioctl(fd, vidiocEnumFrameintervals, unsafe.Pointer(&fi)) != nil {
			break
		}
		if fi.Type == frmivalDiscrete {
			if fi.U[0] != 0 {
				out = append(out, roundRate(float64(fi.U[1])/float64(fi.U[0])))
			}
			continue
		}
		// stepwise or continuous: min interval is the max rate
		if fi.U[0] != 0 {
			out = append(out, roundRate(float64(fi.U[1])/float64(fi.U[0])))
		}
		if fi.U[2] != 0 {
			out = append(out, roundRate(float64(fi.U[3])/float64(fi.U[2])))
		}
		break
	}
	return out
}

func roundRate(r float64) float64 { return float64(int(r*100+0.5)) / 100 }

func (b Backend) Open(ctx context.Context, id string, cfg camera.Config) (camera.Stream, error) {
	info, err := probe(id)
	if err != nil {
		if errors.Is(err, errNotCapture) {
			return nil, fmt.Errorf("%w: %s is not a camera", camera.ErrNotFound, id)
		}
		return nil, err
	}
	mode, err := camera.ChooseMode(info.Modes, cfg)
	if err != nil {
		return nil, err
	}
	fps := camera.PickFrameRate(mode, cfg.FrameRate)
	return openStream(id, mode, fps, cfg.Buffers)
}

// v4l2Code returns the V4L2 fourcc for a library format.
func v4l2Code(fd int, pf camera.PixelFormat) uint32 {
	// Prefer the code the device actually lists (MJPG vs JPEG).
	for i := uint32(0); ; i++ {
		d := fmtdesc{Index: i, Type: bufTypeVideoCapture}
		if ioctl(fd, vidiocEnumFmt, unsafe.Pointer(&d)) != nil {
			break
		}
		if formats[d.PixelFormat] == pf {
			return d.PixelFormat
		}
	}
	return 0
}

type mapping struct {
	mem []byte
}

type stream struct {
	path    string
	fd      int
	efd     int // eventfd that wakes a blocked Read on Close
	mode    camera.Mode
	stride  int
	bufs    []mapping
	start   time.Duration
	started bool

	readMu sync.Mutex // one Read at a time; Close waits for it

	mu          sync.Mutex
	closed      bool
	outstanding int
	unmapped    bool
}

func openStream(path string, mode camera.Mode, fps float64, nbuf int) (_ *stream, err error) {
	fd, err := openNode(path)
	if err != nil {
		return nil, err
	}
	s := &stream{path: path, fd: fd, efd: -1}
	defer func() {
		if err != nil {
			s.teardown()
		}
	}()
	code := v4l2Code(fd, mode.Format)
	if code == 0 {
		return nil, fmt.Errorf("%w: %s on %s", camera.ErrUnsupported, mode.Format, path)
	}
	var f format
	f.Type = bufTypeVideoCapture
	p := f.pix()
	p.Width, p.Height, p.PixelFormat, p.Field = uint32(mode.Width), uint32(mode.Height), code, fieldAny
	if err := ioctl(fd, vidiocSFmt, unsafe.Pointer(&f)); err != nil {
		if errors.Is(err, unix.EBUSY) {
			return nil, fmt.Errorf("%w: %s", camera.ErrBusy, path)
		}
		return nil, &camera.BackendError{Backend: "v4l2", Op: "VIDIOC_S_FMT", Err: err}
	}
	// The driver may have adjusted the request.
	got, ok := formats[p.PixelFormat]
	if !ok {
		return nil, fmt.Errorf("%w: driver chose %q", camera.ErrUnsupported, fourccString(p.PixelFormat))
	}
	s.mode = camera.Mode{Format: got, Width: int(p.Width), Height: int(p.Height)}
	s.stride = int(p.BytesPerLine)
	if s.stride == 0 && got.Raw() {
		s.stride = minStride(got, s.mode.Width)
	}

	if fps > 0 {
		var sp streamparm
		sp.Type = bufTypeVideoCapture
		if ioctl(fd, vidiocGParm, unsafe.Pointer(&sp)) == nil && sp.capture().Capability&capTimePerFrame != 0 {
			c := sp.capture()
			c.TimePerFrame = rateToInterval(fps)
			_ = ioctl(fd, vidiocSParm, unsafe.Pointer(&sp))
		}
		sp = streamparm{Type: bufTypeVideoCapture}
		if ioctl(fd, vidiocGParm, unsafe.Pointer(&sp)) == nil {
			if t := sp.capture().TimePerFrame; t[0] != 0 {
				s.mode.FrameRates = []float64{roundRate(float64(t[1]) / float64(t[0]))}
			}
		}
	}

	if nbuf <= 0 {
		nbuf = 4
	}
	rb := requestbuffers{Count: uint32(nbuf), Type: bufTypeVideoCapture, Memory: memoryMMAP}
	if err := ioctl(fd, vidiocReqbufs, unsafe.Pointer(&rb)); err != nil {
		if errors.Is(err, unix.EBUSY) {
			return nil, fmt.Errorf("%w: %s", camera.ErrBusy, path)
		}
		return nil, &camera.BackendError{Backend: "v4l2", Op: "VIDIOC_REQBUFS", Err: err}
	}
	if rb.Count < 2 {
		return nil, &camera.BackendError{Backend: "v4l2", Op: "VIDIOC_REQBUFS", Err: fmt.Errorf("driver granted %d buffers", rb.Count)}
	}
	for i := uint32(0); i < rb.Count; i++ {
		bf := buffer{Index: i, Type: bufTypeVideoCapture, Memory: memoryMMAP}
		if err := ioctl(fd, vidiocQuerybuf, unsafe.Pointer(&bf)); err != nil {
			return nil, &camera.BackendError{Backend: "v4l2", Op: "VIDIOC_QUERYBUF", Err: err}
		}
		mem, err := unix.Mmap(fd, int64(uint32(bf.M)), int(bf.Length), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return nil, &camera.BackendError{Backend: "v4l2", Op: "mmap", Err: err}
		}
		s.bufs = append(s.bufs, mapping{mem: mem})
	}
	for i := range s.bufs {
		bf := buffer{Index: uint32(i), Type: bufTypeVideoCapture, Memory: memoryMMAP}
		if err := ioctl(fd, vidiocQbuf, unsafe.Pointer(&bf)); err != nil {
			return nil, &camera.BackendError{Backend: "v4l2", Op: "VIDIOC_QBUF", Err: err}
		}
	}
	s.efd, err = unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return nil, &camera.BackendError{Backend: "v4l2", Op: "eventfd", Err: err}
	}
	typ := int32(bufTypeVideoCapture)
	if err := ioctl(fd, vidiocStreamon, unsafe.Pointer(&typ)); err != nil {
		if errors.Is(err, unix.ENOSPC) {
			// USB bandwidth: another camera on the same bus, or an
			// uncompressed mode too large for USB 2.
			return nil, fmt.Errorf("%w: %s: not enough USB bandwidth for %s (try MJPEG or a smaller size)", camera.ErrBusy, path, s.mode)
		}
		return nil, &camera.BackendError{Backend: "v4l2", Op: "VIDIOC_STREAMON", Err: err}
	}
	return s, nil
}

func rateToInterval(fps float64) [2]uint32 {
	// Integer rates map to 1/fps; NTSC rates (29.97...) to 1001/30000.
	if fps == float64(int(fps)) {
		return [2]uint32{1, uint32(fps)}
	}
	return [2]uint32{1000, uint32(fps*1000 + 0.5)}
}

func minStride(f camera.PixelFormat, w int) int {
	switch f {
	case camera.YUYV, camera.UYVY:
		return 2 * w
	case camera.RGBA, camera.BGRA:
		return 4 * w
	}
	return w
}

func (s *stream) Mode() camera.Mode { return s.mode }

func (s *stream) Read(ctx context.Context) (*camera.Frame, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil, camera.ErrClosed
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bf := buffer{Type: bufTypeVideoCapture, Memory: memoryMMAP}
		err := ioctl(s.fd, vidiocDqbuf, unsafe.Pointer(&bf))
		if err == unix.EAGAIN {
			if err := s.wait(ctx); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			if errors.Is(err, unix.ENODEV) {
				return nil, fmt.Errorf("%w: %s was unplugged", camera.ErrClosed, s.path)
			}
			return nil, &camera.BackendError{Backend: "v4l2", Op: "VIDIOC_DQBUF", Err: err}
		}
		if bf.Flags&bufFlagError != 0 || bf.BytesUsed == 0 {
			// A corrupted frame (USB packet loss): give it back and wait for
			// the next one.
			s.requeue(bf.Index)
			continue
		}
		return s.frame(&bf), nil
	}
}

// wait blocks until the device has a frame, Close is called, or ctx is done.
func (s *stream) wait(ctx context.Context) error {
	fds := []unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLIN}, {Fd: int32(s.efd), Events: unix.POLLIN}}
	for {
		timeout := 100 // ms, to notice ctx cancellation
		if dl, ok := ctx.Deadline(); ok {
			if d := time.Until(dl); d < 100*time.Millisecond {
				timeout = max(int(d/time.Millisecond), 0)
			}
		}
		n, err := unix.Poll(fds, timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return &camera.BackendError{Backend: "v4l2", Op: "poll", Err: err}
		}
		if fds[1].Revents != 0 {
			return camera.ErrClosed
		}
		if n > 0 {
			if fds[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0 && fds[0].Revents&unix.POLLIN == 0 {
				return fmt.Errorf("%w: %s stopped streaming", camera.ErrClosed, s.path)
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (s *stream) frame(bf *buffer) *camera.Frame {
	ts := time.Duration(bf.Timestamp.Sec)*time.Second + time.Duration(bf.Timestamp.Usec)*time.Microsecond
	if !s.started {
		s.start, s.started = ts, true
	}
	mem := s.bufs[bf.Index].mem
	used := int(bf.BytesUsed)
	if used > len(mem) {
		used = len(mem)
	}
	f := &camera.Frame{
		Width: s.mode.Width, Height: s.mode.Height, Format: s.mode.Format,
		Timestamp: ts - s.start, Sequence: uint64(bf.Sequence),
	}
	h, st := s.mode.Height, s.stride
	switch s.mode.Format {
	case camera.MJPEG:
		f.Planes, f.Strides = [][]byte{mem[:used]}, []int{0}
	case camera.NV12:
		f.Planes = [][]byte{mem[:st*h], mem[st*h : min(st*h+st*((h+1)/2), len(mem))]}
		f.Strides = []int{st, st}
	case camera.I420:
		cs := st / 2
		ch := (h + 1) / 2
		y := st * h
		f.Planes = [][]byte{mem[:y], mem[y : y+cs*ch], mem[y+cs*ch : min(y+2*cs*ch, len(mem))]}
		f.Strides = []int{st, cs, cs}
	default:
		f.Planes, f.Strides = [][]byte{mem[:min(st*h, len(mem))]}, []int{st}
	}
	idx := bf.Index
	s.mu.Lock()
	s.outstanding++
	s.mu.Unlock()
	camera.SetRelease(f, func() {
		s.mu.Lock()
		s.outstanding--
		if !s.closed {
			// Under the lock so Close cannot close the fd in between.
			s.requeue(idx)
		}
		unmap := s.closed && s.outstanding == 0
		s.mu.Unlock()
		if unmap {
			s.unmap()
		}
	})
	return f
}

func (s *stream) requeue(idx uint32) {
	bf := buffer{Index: idx, Type: bufTypeVideoCapture, Memory: memoryMMAP}
	_ = ioctl(s.fd, vidiocQbuf, unsafe.Pointer(&bf))
}

func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.efd >= 0 {
		var one = [8]byte{1}
		_, _ = unix.Write(s.efd, one[:])
	}
	s.readMu.Lock() // wait for a blocked Read to return
	defer s.readMu.Unlock()
	typ := int32(bufTypeVideoCapture)
	_ = ioctl(s.fd, vidiocStreamoff, unsafe.Pointer(&typ))
	s.teardown()
	return nil
}

// teardown releases the device. Mapped buffers stay until every frame
// handed out has been released.
func (s *stream) teardown() {
	s.mu.Lock()
	s.closed = true
	pending := s.outstanding
	s.mu.Unlock()
	if pending == 0 {
		s.unmap()
	}
	if s.efd >= 0 {
		unix.Close(s.efd)
		s.efd = -1
	}
	s.mu.Lock()
	if s.fd >= 0 {
		// The kernel keeps the driver buffers of live mappings after the fd
		// is closed and frees them at the last munmap.
		unix.Close(s.fd)
		s.fd = -1
	}
	s.mu.Unlock()
}

func (s *stream) unmap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unmapped {
		return
	}
	s.unmapped = true
	for _, b := range s.bufs {
		_ = unix.Munmap(b.mem)
	}
	s.bufs = nil
}

// Exists reports whether any /dev/video node is present (for diagnostics).
func Exists() bool {
	_, err := os.Stat("/dev/video0")
	return err == nil || len(nodes()) > 0
}

//go:build linux && (amd64 || arm64 || riscv64 || loong64 || ppc64le || s390x)

// Package pulse is the Linux audio capture backend: the PulseAudio client
// library, loaded at run time with purego. Desktop distributions run
// either PulseAudio or PipeWire's PulseAudio server (pipewire-pulse), and
// libpulse talks to both, so this reaches every microphone the desktop
// sees, with resampling and channel mixing done by the sound server. Raw
// ALSA devices are usually held by that server and are not opened directly.
//
// Capture goes through the blocking "simple" API (pa_simple); the device
// list through the asynchronous API with a private pa_mainloop.
package pulse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	iaudio "github.com/shibukawa/hwcamera/internal/audio"
)

const (
	streamRecord = 2

	sampleS16LE = 3
	sampleF32LE = 5

	contextNoAutospawn = 1
	contextReady       = 4
	contextFailed      = 5
	contextTerminated  = 6

	operationRunning = 0

	errAccess    = 1  // PA_ERR_ACCESS
	errNoEntity  = 5  // PA_ERR_NOENTITY
	errConnRefus = 6  // PA_ERR_CONNECTIONREFUSED
	errBusy      = 10 // PA_ERR_BUSY
)

type sampleSpec struct {
	Format   int32
	Rate     uint32
	Channels uint8
	_        [3]byte
}

type bufferAttr struct {
	MaxLength uint32
	TLength   uint32
	PreBuf    uint32
	MinReq    uint32
	FragSize  uint32
}

// Offsets into struct pa_source_info and struct pa_server_info (pulse/
// introspect.h, 64-bit). Only the leading fields are read; their layout has
// not changed since PulseAudio 0.9. pa_source_info: name, index,
// description, sample_spec (12 bytes at 24), channel_map (1 + 32 positions
// at 36), owner_module (168), volume (pa_cvolume, 1 + 32 values at 172),
// mute (304), monitor_of_sink (308). Checked against libpulse 17 talking to
// pipewire-pulse: the monitor source carries its sink's index there and
// the microphone PA_INVALID_INDEX.
const (
	srcName         = 0
	srcIndex        = 8
	srcDescription  = 16
	srcRate         = 28
	srcChannels     = 32
	srcMonitorOfSnk = 308

	srvDefaultSource = 56
)

var (
	loadOnce sync.Once
	loadErr  error

	paSimpleNew   func(server, name *byte, dir int32, dev, streamName *byte, ss *sampleSpec, chmap uintptr, attr *bufferAttr, errp *int32) uintptr
	paSimpleRead  func(s uintptr, data unsafe.Pointer, n uintptr, errp *int32) int32
	paSimpleFree  func(s uintptr)
	paSimpleFlush func(s uintptr, errp *int32) int32
	paStrerror    func(e int32) string

	paMainloopNew      func() uintptr
	paMainloopGetAPI   func(m uintptr) uintptr
	paMainloopIterate  func(m uintptr, block int32, retval *int32) int32
	paMainloopFree     func(m uintptr)
	paContextNew       func(api uintptr, name string) uintptr
	paContextConnect   func(c uintptr, server uintptr, flags int32, api uintptr) int32
	paContextGetState  func(c uintptr) int32
	paContextErrno     func(c uintptr) int32
	paContextDisconn   func(c uintptr)
	paContextUnref     func(c uintptr)
	paGetServerInfo    func(c uintptr, cb uintptr, ud uintptr) uintptr
	paGetSourceInfoLst func(c uintptr, cb uintptr, ud uintptr) uintptr
	paOperationState   func(o uintptr) int32
	paOperationUnref   func(o uintptr)

	serverInfoCB uintptr
	sourceInfoCB uintptr
)

func load() error {
	loadOnce.Do(func() {
		lib, err := purego.Dlopen("libpulse.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("libpulse.so.0: %w", err)
			return
		}
		simple, err := purego.Dlopen("libpulse-simple.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("libpulse-simple.so.0: %w", err)
			return
		}
		purego.RegisterLibFunc(&paSimpleNew, simple, "pa_simple_new")
		purego.RegisterLibFunc(&paSimpleRead, simple, "pa_simple_read")
		purego.RegisterLibFunc(&paSimpleFree, simple, "pa_simple_free")
		purego.RegisterLibFunc(&paSimpleFlush, simple, "pa_simple_flush")
		purego.RegisterLibFunc(&paStrerror, lib, "pa_strerror")
		purego.RegisterLibFunc(&paMainloopNew, lib, "pa_mainloop_new")
		purego.RegisterLibFunc(&paMainloopGetAPI, lib, "pa_mainloop_get_api")
		purego.RegisterLibFunc(&paMainloopIterate, lib, "pa_mainloop_iterate")
		purego.RegisterLibFunc(&paMainloopFree, lib, "pa_mainloop_free")
		purego.RegisterLibFunc(&paContextNew, lib, "pa_context_new")
		purego.RegisterLibFunc(&paContextConnect, lib, "pa_context_connect")
		purego.RegisterLibFunc(&paContextGetState, lib, "pa_context_get_state")
		purego.RegisterLibFunc(&paContextErrno, lib, "pa_context_errno")
		purego.RegisterLibFunc(&paContextDisconn, lib, "pa_context_disconnect")
		purego.RegisterLibFunc(&paContextUnref, lib, "pa_context_unref")
		purego.RegisterLibFunc(&paGetServerInfo, lib, "pa_context_get_server_info")
		purego.RegisterLibFunc(&paGetSourceInfoLst, lib, "pa_context_get_source_info_list")
		purego.RegisterLibFunc(&paOperationState, lib, "pa_operation_get_state")
		purego.RegisterLibFunc(&paOperationUnref, lib, "pa_operation_unref")
		// Callbacks are a scarce resource in purego: create each once and
		// route their results through the enumeration in progress.
		serverInfoCB = purego.NewCallback(func(c, info, ud unsafe.Pointer) {
			if info != nil && cur != nil {
				cur.defaultSource = gostr(*(*unsafe.Pointer)(unsafe.Add(info, srvDefaultSource)))
			}
		})
		sourceInfoCB = purego.NewCallback(func(c, info unsafe.Pointer, eol uintptr, ud unsafe.Pointer) {
			if int32(eol) != 0 || info == nil || cur == nil {
				return
			}
			p := info
			cur.sources = append(cur.sources, source{
				name:     gostr(*(*unsafe.Pointer)(unsafe.Add(p, srcName))),
				desc:     gostr(*(*unsafe.Pointer)(unsafe.Add(p, srcDescription))),
				rate:     *(*uint32)(unsafe.Add(p, srcRate)),
				channels: *(*uint8)(unsafe.Add(p, srcChannels)),
				monitor:  *(*uint32)(unsafe.Add(p, srcMonitorOfSnk)) != 0xffffffff,
			})
		})
	})
	return loadErr
}

func gostr(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	var b []byte
	for i := 0; ; i++ {
		c := *(*byte)(unsafe.Add(p, i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

type source struct {
	name, desc string
	rate       uint32
	channels   uint8
	monitor    bool
}

type enumeration struct {
	defaultSource string
	sources       []source
}

var (
	enumMu sync.Mutex
	cur    *enumeration // the enumeration the callbacks fill; guarded by enumMu
)

// Backend is the PulseAudio backend.
type Backend struct{}

func (Backend) Name() string { return "pulse" }

func (Backend) Available() bool { return load() == nil }

func (Backend) Devices(ctx context.Context) ([]iaudio.DeviceInfo, error) {
	e, err := enumerate(ctx)
	if err != nil {
		return nil, err
	}
	var out []iaudio.DeviceInfo
	for _, s := range e.sources {
		out = append(out, iaudio.DeviceInfo{
			ID: s.name, Name: s.desc, Backend: "pulse",
			Default: s.name == e.defaultSource, Monitor: s.monitor,
			SampleRate: int(s.rate), Channels: int(s.channels),
		})
	}
	// Microphones first, the default leading.
	sortDevices(out)
	return out, nil
}

func sortDevices(ds []iaudio.DeviceInfo) {
	rank := func(d iaudio.DeviceInfo) int {
		switch {
		case d.Default:
			return 0
		case !d.Monitor:
			return 1
		}
		return 2
	}
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && rank(ds[j]) < rank(ds[j-1]); j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
}

func enumerate(ctx context.Context) (*enumeration, error) {
	if err := load(); err != nil {
		return nil, fmt.Errorf("%w: %v", iaudio.ErrUnsupported, err)
	}
	enumMu.Lock()
	defer enumMu.Unlock()
	e := &enumeration{}
	cur = e
	defer func() { cur = nil }()

	ml := paMainloopNew()
	if ml == 0 {
		return nil, &iaudio.BackendError{Backend: "pulse", Op: "pa_mainloop_new"}
	}
	defer paMainloopFree(ml)
	c := paContextNew(paMainloopGetAPI(ml), "hwcamera")
	if c == 0 {
		return nil, &iaudio.BackendError{Backend: "pulse", Op: "pa_context_new"}
	}
	defer paContextUnref(c)
	if paContextConnect(c, 0, contextNoAutospawn, 0) < 0 {
		return nil, paErr("pa_context_connect", paContextErrno(c))
	}
	defer paContextDisconn(c)

	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	iterate := func(done func() bool) error {
		for !done() {
			if paMainloopIterate(ml, 0, nil) < 0 {
				return paErr("pa_mainloop_iterate", paContextErrno(c))
			}
			if done() {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return &iaudio.BackendError{Backend: "pulse", Op: "sound server", Err: errors.New("no answer")}
			}
			time.Sleep(time.Millisecond)
		}
		return nil
	}
	var connErr error
	if err := iterate(func() bool {
		switch paContextGetState(c) {
		case contextReady:
			return true
		case contextFailed, contextTerminated:
			connErr = paErr("connect to the sound server", paContextErrno(c))
			return true
		}
		return false
	}); err != nil {
		return nil, err
	}
	if connErr != nil {
		return nil, connErr
	}
	for _, start := range []func() uintptr{
		func() uintptr { return paGetServerInfo(c, serverInfoCB, 0) },
		func() uintptr { return paGetSourceInfoLst(c, sourceInfoCB, 0) },
	} {
		op := start()
		if op == 0 {
			return nil, paErr("introspect", paContextErrno(c))
		}
		err := iterate(func() bool { return paOperationState(op) != operationRunning })
		paOperationUnref(op)
		if err != nil {
			return nil, err
		}
	}
	return e, nil
}

func paErr(op string, code int32) error {
	var base error
	switch code {
	case errAccess:
		base = iaudio.ErrPermission
	case errNoEntity:
		base = iaudio.ErrNotFound
	case errBusy:
		base = iaudio.ErrBusy
	case errConnRefus:
		base = fmt.Errorf("%w: no PulseAudio or PipeWire sound server running", iaudio.ErrUnsupported)
	}
	msg := ""
	if paStrerror != nil {
		msg = paStrerror(code)
	}
	if base != nil {
		return &iaudio.BackendError{Backend: "pulse", Op: op, Code: int64(code), Err: fmt.Errorf("%w (%s)", base, msg)}
	}
	return &iaudio.BackendError{Backend: "pulse", Op: op, Code: int64(code), Err: errors.New(msg)}
}

func (b Backend) Open(ctx context.Context, id string, cfg iaudio.Config) (iaudio.Stream, error) {
	if err := load(); err != nil {
		return nil, fmt.Errorf("%w: %v", iaudio.ErrUnsupported, err)
	}
	if cfg.Channels > 32 || cfg.Channels < 1 {
		return nil, fmt.Errorf("%w: %d channels", iaudio.ErrUnsupported, cfg.Channels)
	}
	ss := sampleSpec{Format: sampleS16LE, Rate: uint32(cfg.SampleRate), Channels: uint8(cfg.Channels)}
	if cfg.Format == iaudio.F32 {
		ss.Format = sampleF32LE
	}
	frameBytes := cfg.Channels * cfg.Format.Size()
	period := cfg.PeriodFrames() * frameBytes
	// fragsize asks the server to deliver one period at a time, which keeps
	// the capture latency at about a period.
	attr := bufferAttr{MaxLength: ^uint32(0), TLength: ^uint32(0), PreBuf: ^uint32(0), MinReq: ^uint32(0), FragSize: uint32(period)}
	var dev *byte
	if id != "" {
		// PipeWire's server records from the default source instead of
		// failing when the named one does not exist, so check first.
		e, err := enumerate(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, s := range e.sources {
			found = found || s.name == id
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", iaudio.ErrNotFound, id)
		}
		dev = cstr(id)
	}
	var code int32
	h := paSimpleNew(nil, cstr("hwcamera"), streamRecord, dev, cstr("capture"), &ss, 0, &attr, &code)
	if h == 0 {
		err := paErr("pa_simple_new", code)
		if id != "" && code == errNoEntity {
			return nil, fmt.Errorf("%w: %s", iaudio.ErrNotFound, id)
		}
		return nil, err
	}
	s := &stream{h: h, cfg: cfg, period: period, frameBytes: frameBytes, ring: iaudio.NewRing(50), done: make(chan struct{})}
	go s.run()
	return s, nil
}

type stream struct {
	h          uintptr
	cfg        iaudio.Config
	period     int
	frameBytes int
	ring       *iaudio.Ring
	frames     int64

	closeOnce sync.Once
	quit      sync.Mutex
	closing   bool
	done      chan struct{}
}

func (s *stream) Config() iaudio.Config { return s.cfg }

// run reads periods on its own goroutine: pa_simple_read blocks, and the
// handle must not be freed while a read is in flight.
func (s *stream) run() {
	defer close(s.done)
	defer paSimpleFree(s.h)
	for {
		s.quit.Lock()
		closing := s.closing
		s.quit.Unlock()
		if closing {
			s.ring.Fail(iaudio.ErrClosed)
			return
		}
		buf := make([]byte, s.period)
		var code int32
		if paSimpleRead(s.h, unsafe.Pointer(&buf[0]), uintptr(len(buf)), &code) < 0 {
			// The sound server went away or the source was removed.
			s.ring.Fail(fmt.Errorf("%w: %v", iaudio.ErrClosed, paErr("pa_simple_read", code)))
			return
		}
		n := len(buf) / s.frameBytes
		s.ring.Push(&iaudio.Buffer{
			Data: buf, Format: s.cfg.Format, SampleRate: s.cfg.SampleRate, Channels: s.cfg.Channels,
			Frames:    n,
			Timestamp: time.Duration(s.frames) * time.Second / time.Duration(s.cfg.SampleRate),
		})
		s.frames += int64(n)
	}
}

func (s *stream) Read(ctx context.Context) (*iaudio.Buffer, error) {
	s.quit.Lock()
	closing := s.closing
	s.quit.Unlock()
	if closing {
		return nil, iaudio.ErrClosed
	}
	return s.ring.Pop(ctx)
}

// Close stops the reader after its current period (at most one period of
// waiting) and frees the stream.
func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		s.quit.Lock()
		s.closing = true
		s.quit.Unlock()
		s.ring.Fail(iaudio.ErrClosed)
		<-s.done
	})
	return nil
}

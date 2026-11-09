//go:build windows && (amd64 || arm64)

package wasapi

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/shibukawa/hwcamera/internal/audio"
)

// Available reports whether the MMDevice API and the COM entry points used
// by the backend can be loaded.
func (Backend) Available() bool {
	return modMmdevapi.Load() == nil && procCoCreateInstance.Find() == nil && procPropVariantClear.Find() == nil
}

// release releases a COM interface pointer of any of the view types; it is
// a no-op on nil.
func release[T any](p *T) {
	if p != nil {
		(*iunknown)(unsafe.Pointer(p)).Release()
	}
}

// coInit joins the calling (OS-locked) thread to the multithreaded
// apartment. uninit reports whether CoUninitialize must be called.
func coInit() (uninit bool, err error) {
	e := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	if e == nil {
		return true, nil
	}
	if errno, ok := e.(syscall.Errno); ok {
		switch HRESULT(uint32(errno)) {
		case S_FALSE:
			return true, nil
		case RPC_E_CHANGED_MODE:
			// The thread is in an STA; COM calls still work on it.
			return false, nil
		}
		return false, hrError("CoInitializeEx", HRESULT(uint32(errno)))
	}
	return false, &audio.BackendError{Backend: backendName, Op: "CoInitializeEx", Err: e}
}

// withCOM runs fn on a fresh OS thread joined to the MTA and waits for it.
// The thread is never unlocked, so it exits with the goroutine and no COM
// state leaks into the Go scheduler's thread pool.
func withCOM(fn func() error) error {
	ch := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		uninit, err := coInit()
		if err != nil {
			ch <- err
			return
		}
		err = fn()
		if uninit {
			windows.CoUninitialize()
		}
		ch <- err
	}()
	return <-ch
}

func newEnumerator() (*IMMDeviceEnumerator, error) {
	var en *IMMDeviceEnumerator
	if hr := coCreateInstance(&CLSID_MMDeviceEnumerator, clsctxAll, &IID_IMMDeviceEnumerator, unsafe.Pointer(&en)); hr.Failed() {
		return nil, hrError("CoCreateInstance(MMDeviceEnumerator)", hr)
	}
	return en, nil
}

// deviceID returns the endpoint ID string of d, or "".
func deviceID(d *IMMDevice) string {
	var p *uint16
	if hr := d.GetId(&p); hr.Failed() || p == nil {
		return ""
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p)
}

// friendlyName returns PKEY_Device_FriendlyName of d, or "".
func friendlyName(d *IMMDevice) string {
	var ps *IPropertyStore
	if hr := d.OpenPropertyStore(stgmRead, &ps); hr.Failed() || ps == nil {
		return ""
	}
	defer release(ps)
	var pv PROPVARIANT
	if hr := ps.GetValue(&PKEY_Device_FriendlyName, &pv); hr.Failed() {
		return ""
	}
	defer pv.Clear()
	if pv.VT != vtLPWSTR || pv.Val == nil {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(pv.Val))
}

// mixFormat returns the shared-mode engine format of d (0, 0 if unknown).
// The audio client is only activated to query it, never initialized.
func mixFormat(d *IMMDevice) (sampleRate, channels int) {
	var client *IAudioClient
	if hr := d.Activate(&IID_IAudioClient, clsctxAll, unsafe.Pointer(&client)); hr.Failed() || client == nil {
		return 0, 0
	}
	defer release(client)
	var wf unsafe.Pointer
	if hr := client.GetMixFormat(&wf); hr.Failed() || wf == nil {
		return 0, 0
	}
	defer windows.CoTaskMemFree(wf)
	rate, ch, _ := parseWaveFormat(unsafe.Slice((*byte)(wf), sizeofWAVEFORMATEX))
	return rate, ch
}

// Devices lists the active capture endpoints.
func (b Backend) Devices(ctx context.Context) ([]audio.DeviceInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !b.Available() {
		return nil, fmt.Errorf("%w: WASAPI is not available", audio.ErrUnsupported)
	}
	var out []audio.DeviceInfo
	err := withCOM(func() error {
		en, err := newEnumerator()
		if err != nil {
			return err
		}
		defer release(en)

		defID := ""
		var def *IMMDevice
		if hr := en.GetDefaultAudioEndpoint(eCapture, eConsole, &def); !hr.Failed() && def != nil {
			defID = deviceID(def)
			release(def)
		}

		var col *IMMDeviceCollection
		if hr := en.EnumAudioEndpoints(eCapture, DEVICE_STATE_ACTIVE, &col); hr.Failed() {
			return hrError("EnumAudioEndpoints", hr)
		}
		defer release(col)
		var n uint32
		if hr := col.GetCount(&n); hr.Failed() {
			return hrError("IMMDeviceCollection.GetCount", hr)
		}
		for i := range n {
			var d *IMMDevice
			if hr := col.Item(i, &d); hr.Failed() || d == nil {
				continue
			}
			id := deviceID(d)
			if id == "" {
				release(d)
				continue
			}
			rate, ch := mixFormat(d)
			out = append(out, audio.DeviceInfo{
				ID:         id,
				Name:       friendlyName(d),
				Backend:    backendName,
				Default:    id == defID,
				SampleRate: rate,
				Channels:   ch,
			})
			release(d)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Open opens the capture endpoint id ("" for the default console capture
// endpoint) delivering exactly cfg's format.
func (b Backend) Open(ctx context.Context, id string, cfg audio.Config) (audio.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !b.Available() {
		return nil, fmt.Errorf("%w: WASAPI is not available", audio.ErrUnsupported)
	}
	cfg.Normalize()
	if cfg.Format != audio.S16 && cfg.Format != audio.F32 {
		return nil, fmt.Errorf("%w: sample format %v", audio.ErrUnsupported, cfg.Format)
	}
	if cfg.Channels > 0xFFFF || cfg.Channels*cfg.Format.Size() > 0xFFFF {
		return nil, fmt.Errorf("%w: %d channels", audio.ErrUnsupported, cfg.Channels)
	}
	quit, err := windows.CreateEvent(nil, 1, 0, nil) // manual reset
	if err != nil {
		return nil, &audio.BackendError{Backend: backendName, Op: "CreateEvent", Err: err}
	}
	s := &stream{
		cfg:  cfg,
		ring: audio.NewRing(ringSize(cfg)),
		quit: quit,
		done: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go s.run(id, ready)
	if err := <-ready; err != nil {
		<-s.done
		windows.CloseHandle(quit)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

type stream struct {
	cfg  audio.Config
	ring *audio.Ring
	quit windows.Handle // manual-reset; set by Close
	done chan struct{}  // closed when the worker has released everything

	closeOnce sync.Once
	closed    atomic.Bool
}

func (s *stream) Config() audio.Config { return s.cfg }

// Read blocks for the next period of audio.
func (s *stream) Read(ctx context.Context) (*audio.Buffer, error) {
	if s.closed.Load() {
		return nil, audio.ErrClosed
	}
	b, err := s.ring.Pop(ctx)
	if err != nil {
		if s.closed.Load() {
			return nil, audio.ErrClosed
		}
		return nil, err
	}
	return b, nil
}

// Close stops capture and waits for the worker to release the device.
func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		windows.SetEvent(s.quit)
		<-s.done
		s.ring.Fail(audio.ErrClosed)
		windows.CloseHandle(s.quit)
	})
	return nil
}

// session holds the COM objects of one capture stream. All of it lives on
// the worker's OS thread.
type session struct {
	en      *IMMDeviceEnumerator
	dev     *IMMDevice
	client  *IAudioClient
	capture *IAudioCaptureClient
	event   windows.Handle
	started bool
}

func (c *session) release() {
	if c.started {
		c.client.Stop()
	}
	release(c.capture)
	release(c.client)
	release(c.dev)
	release(c.en)
	if c.event != 0 {
		windows.CloseHandle(c.event)
	}
	*c = session{}
}

func (c *session) open(id string, cfg audio.Config) error {
	en, err := newEnumerator()
	if err != nil {
		return err
	}
	c.en = en

	if id == "" {
		if hr := en.GetDefaultAudioEndpoint(eCapture, eConsole, &c.dev); hr.Failed() || c.dev == nil {
			return lookupError("GetDefaultAudioEndpoint", hr, audio.ErrNoDevice)
		}
	} else {
		wid, err := windows.UTF16PtrFromString(id)
		if err != nil {
			return fmt.Errorf("%w: %q", audio.ErrNotFound, id)
		}
		if hr := en.GetDevice(wid, &c.dev); hr.Failed() || c.dev == nil {
			return lookupError("GetDevice", hr, audio.ErrNotFound)
		}
		// GetDevice also resolves render endpoints and inactive devices.
		var ep *IMMEndpoint
		if hr := c.dev.queryInterface(&IID_IMMEndpoint, unsafe.Pointer(&ep)); !hr.Failed() && ep != nil {
			var flow uint32
			hr := ep.GetDataFlow(&flow)
			release(ep)
			if !hr.Failed() && flow != eCapture {
				return fmt.Errorf("%w: %q is not a capture endpoint", audio.ErrNotFound, id)
			}
		}
		var state uint32
		if hr := c.dev.GetState(&state); !hr.Failed() && state&DEVICE_STATE_ACTIVE == 0 {
			return fmt.Errorf("%w: %q is not active (state 0x%x)", audio.ErrNotFound, id, state)
		}
	}

	if hr := c.dev.Activate(&IID_IAudioClient, clsctxAll, unsafe.Pointer(&c.client)); hr.Failed() || c.client == nil {
		return hrError("IMMDevice.Activate(IAudioClient)", hr)
	}

	// 8-byte aligned storage for the packed WAVEFORMATEXTENSIBLE.
	var wfx struct {
		_ [0]uint64
		b [sizeofWAVEFORMATEXTENSIBLE]byte
	}
	wfx.b = waveFormatExtensible(cfg)
	const flags = AUDCLNT_STREAMFLAGS_EVENTCALLBACK | AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY
	hr := c.client.Initialize(AUDCLNT_SHAREMODE_SHARED, flags, bufferDuration(cfg), 0, unsafe.Pointer(&wfx.b[0]), nil)
	runtime.KeepAlive(&wfx)
	if hr.Failed() {
		return hrError("IAudioClient.Initialize", hr)
	}

	ev, err := windows.CreateEvent(nil, 0, 0, nil) // auto reset
	if err != nil {
		return &audio.BackendError{Backend: backendName, Op: "CreateEvent", Err: err}
	}
	c.event = ev
	if hr := c.client.SetEventHandle(ev); hr.Failed() {
		return hrError("IAudioClient.SetEventHandle", hr)
	}
	if hr := c.client.GetService(&IID_IAudioCaptureClient, unsafe.Pointer(&c.capture)); hr.Failed() || c.capture == nil {
		return hrError("IAudioClient.GetService(IAudioCaptureClient)", hr)
	}
	if hr := c.client.Start(); hr.Failed() {
		return hrError("IAudioClient.Start", hr)
	}
	c.started = true
	return nil
}

// run is the worker: it owns the OS thread, the COM apartment and every
// interface of the stream.
func (s *stream) run(id string, ready chan<- error) {
	defer close(s.done)
	// Never unlocked: the thread exits with the goroutine.
	runtime.LockOSThread()
	uninit, err := coInit()
	if err != nil {
		ready <- err
		return
	}
	var c session
	err = c.open(id, s.cfg)
	if err != nil {
		c.release()
		if uninit {
			windows.CoUninitialize()
		}
		ready <- err
		return
	}
	ready <- nil

	err = s.loop(&c)
	c.release()
	if uninit {
		windows.CoUninitialize()
	}
	if err == nil {
		err = audio.ErrClosed
	}
	s.ring.Fail(err)
}

const (
	waitObject0 = 0x000
	waitTimeout = 0x102
)

func (s *stream) loop(c *session) error {
	acc := newAccumulator(s.cfg)
	ts := timestamper{rate: int64(s.cfg.SampleRate)}
	handles := []windows.Handle{s.quit, c.event}
	frameBytes := s.cfg.Channels * s.cfg.Format.Size()
	for {
		ev, err := windows.WaitForMultipleObjects(handles, false, 200)
		if err != nil {
			return &audio.BackendError{Backend: backendName, Op: "WaitForMultipleObjects", Err: err}
		}
		if ev == waitObject0 {
			return nil
		}
		// Data event or timeout: drain every packet. Draining on timeout
		// also surfaces AUDCLNT_E_DEVICE_INVALIDATED when events stop.
		for {
			var n uint32
			if hr := c.capture.GetNextPacketSize(&n); hr.Failed() {
				return hrError("IAudioCaptureClient.GetNextPacketSize", hr)
			}
			if n == 0 {
				break
			}
			var (
				data          *byte
				frames, flags uint32
				devPos, qpc   uint64
			)
			hr := c.capture.GetBuffer(&data, &frames, &flags, &devPos, &qpc)
			if hr == AUDCLNT_S_BUFFER_EMPTY {
				break
			}
			if hr.Failed() {
				return hrError("IAudioCaptureClient.GetBuffer", hr)
			}
			var payload []byte // nil = silence
			if flags&AUDCLNT_BUFFERFLAGS_SILENT == 0 && data != nil && frames > 0 {
				payload = unsafe.Slice(data, int(frames)*frameBytes)
			}
			t := ts.time(int(frames), flags, devPos, qpc)
			// add copies the payload before the buffer is released.
			acc.add(payload, int(frames), t, flags&AUDCLNT_BUFFERFLAGS_DATA_DISCONTINUITY != 0, s.ring.Push)
			if hr := c.capture.ReleaseBuffer(frames); hr.Failed() {
				return hrError("IAudioCaptureClient.ReleaseBuffer", hr)
			}
		}
	}
}

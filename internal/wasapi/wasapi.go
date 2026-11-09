// Package wasapi is the Windows microphone capture backend. It binds the
// Windows Audio Session API (MMDevice enumeration, IAudioClient and
// IAudioCaptureClient) through COM vtables called with syscall.SyscallN, so
// no cgo is required.
//
// This file and accum.go are plain Go so that the GUID tables, result-code
// mapping, WAVEFORMATEXTENSIBLE encoding and period chunking can be unit
// tested on any OS; everything that calls into Windows is in the
// windows-tagged files.
//
// Names of constants, GUIDs and structures follow the Windows SDK headers
// (mmdeviceapi.h, audioclient.h, audiosessiontypes.h, propsys.h,
// functiondiscoverykeys_devpkey.h, mmreg.h, ksmedia.h) so that they can be
// verified against them.
package wasapi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/shibukawa/hwcamera/internal/audio"
)

// Backend is the WASAPI capture backend.
type Backend struct{}

// Name returns "wasapi".
func (Backend) Name() string { return backendName }

const backendName = "wasapi"

// GUID is a COM/Windows GUID with the C layout (16 bytes).
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func (g GUID) String() string {
	return fmt.Sprintf("{%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		g.Data1, g.Data2, g.Data3, g.Data4[0], g.Data4[1],
		g.Data4[2], g.Data4[3], g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// bytes returns the in-memory (little-endian) encoding of g.
func (g GUID) bytes() [16]byte {
	var b [16]byte
	binary.LittleEndian.PutUint32(b[0:], g.Data1)
	binary.LittleEndian.PutUint16(b[4:], g.Data2)
	binary.LittleEndian.PutUint16(b[6:], g.Data3)
	copy(b[8:], g.Data4[:])
	return b
}

func guid(d1 uint32, d2, d3 uint16, b0, b1, b2, b3, b4, b5, b6, b7 byte) GUID {
	return GUID{Data1: d1, Data2: d2, Data3: d3, Data4: [8]byte{b0, b1, b2, b3, b4, b5, b6, b7}}
}

// Class and interface identifiers.
var (
	// mmdeviceapi.h
	CLSID_MMDeviceEnumerator = guid(0xBCDE0395, 0xE52F, 0x467C, 0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E)
	IID_IMMDeviceEnumerator  = guid(0xA95664D2, 0x9614, 0x4F35, 0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6)
	IID_IMMEndpoint          = guid(0x1BE09788, 0x6894, 0x4089, 0x85, 0x86, 0x9A, 0x2A, 0x6C, 0x26, 0x5A, 0xC5)
	// audioclient.h
	IID_IAudioClient        = guid(0x1CB9AD4C, 0xDBFA, 0x4C32, 0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2)
	IID_IAudioCaptureClient = guid(0xC8ADBD64, 0xE71E, 0x48A0, 0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17)
	// ksmedia.h
	KSDATAFORMAT_SUBTYPE_PCM        = guid(0x00000001, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71)
	KSDATAFORMAT_SUBTYPE_IEEE_FLOAT = guid(0x00000003, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71)
)

// PROPERTYKEY mirrors the C struct (wtypes.h): 20 bytes.
type PROPERTYKEY struct {
	FmtID GUID
	PID   uint32
}

// PKEY_Device_FriendlyName (functiondiscoverykeys_devpkey.h).
var PKEY_Device_FriendlyName = PROPERTYKEY{guid(0xA45C254E, 0xDF1C, 0x4EFD, 0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0), 14}

// Enumerations and flags.
const (
	// EDataFlow / ERole (mmdeviceapi.h)
	eRender   = 0
	eCapture  = 1
	eConsole  = 0
	stgmRead  = 0 // STGM_READ (objbase.h)
	vtLPWSTR  = 31
	clsctxAll = 0x17 // CLSCTX_INPROC_SERVER|INPROC_HANDLER|LOCAL_SERVER|REMOTE_SERVER

	DEVICE_STATE_ACTIVE = 0x1

	// AUDCLNT_SHAREMODE (audiosessiontypes.h)
	AUDCLNT_SHAREMODE_SHARED = 0

	AUDCLNT_STREAMFLAGS_EVENTCALLBACK       = 0x00040000
	AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY = 0x08000000
	AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM      = 0x80000000

	// _AUDCLNT_BUFFERFLAGS (audioclient.h)
	AUDCLNT_BUFFERFLAGS_DATA_DISCONTINUITY = 0x1
	AUDCLNT_BUFFERFLAGS_SILENT             = 0x2
	AUDCLNT_BUFFERFLAGS_TIMESTAMP_ERROR    = 0x4

	// mmreg.h
	WAVE_FORMAT_PCM        = 0x0001
	WAVE_FORMAT_IEEE_FLOAT = 0x0003
	WAVE_FORMAT_EXTENSIBLE = 0xFFFE

	SPEAKER_FRONT_LEFT   = 0x1
	SPEAKER_FRONT_RIGHT  = 0x2
	SPEAKER_FRONT_CENTER = 0x4

	// sizes of the packed C structs
	sizeofWAVEFORMATEX         = 18
	sizeofWAVEFORMATEXTENSIBLE = 40
	cbSizeWAVEFORMATEXTENSIBLE = sizeofWAVEFORMATEXTENSIBLE - sizeofWAVEFORMATEX // 22
	sizeofPROPVARIANT64        = 24
)

// HRESULT is a COM result code.
type HRESULT uint32

// Failed reports whether hr is an error code (severity bit set).
func (hr HRESULT) Failed() bool { return hr&0x80000000 != 0 }

func (hr HRESULT) String() string {
	if s, ok := hresultNames[hr]; ok {
		return fmt.Sprintf("%s (0x%08X)", s, uint32(hr))
	}
	return fmt.Sprintf("HRESULT 0x%08X", uint32(hr))
}

// Result codes (winerror.h, audioclient.h).
const (
	S_OK    HRESULT = 0
	S_FALSE HRESULT = 1

	E_NOTIMPL           HRESULT = 0x80004001
	E_NOINTERFACE       HRESULT = 0x80004002
	E_POINTER           HRESULT = 0x80004003
	E_FAIL              HRESULT = 0x80004005
	E_ACCESSDENIED      HRESULT = 0x80070005
	E_OUTOFMEMORY       HRESULT = 0x8007000E
	E_INVALIDARG        HRESULT = 0x80070057
	E_NOTFOUND          HRESULT = 0x80070490 // HRESULT_FROM_WIN32(ERROR_NOT_FOUND)
	E_FILE_NOT_FOUND    HRESULT = 0x80070002 // HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND)
	RPC_E_CHANGED_MODE  HRESULT = 0x80010106
	REGDB_E_CLASSNOTREG HRESULT = 0x80040154

	AUDCLNT_E_NOT_INITIALIZED            HRESULT = 0x88890001
	AUDCLNT_E_ALREADY_INITIALIZED        HRESULT = 0x88890002
	AUDCLNT_E_WRONG_ENDPOINT_TYPE        HRESULT = 0x88890003
	AUDCLNT_E_DEVICE_INVALIDATED         HRESULT = 0x88890004
	AUDCLNT_E_NOT_STOPPED                HRESULT = 0x88890005
	AUDCLNT_E_BUFFER_TOO_LARGE           HRESULT = 0x88890006
	AUDCLNT_E_OUT_OF_ORDER               HRESULT = 0x88890007
	AUDCLNT_E_UNSUPPORTED_FORMAT         HRESULT = 0x88890008
	AUDCLNT_E_INVALID_SIZE               HRESULT = 0x88890009
	AUDCLNT_E_DEVICE_IN_USE              HRESULT = 0x8889000A
	AUDCLNT_E_BUFFER_OPERATION_PENDING   HRESULT = 0x8889000B
	AUDCLNT_E_THREAD_NOT_REGISTERED      HRESULT = 0x8889000C
	AUDCLNT_E_EXCLUSIVE_MODE_NOT_ALLOWED HRESULT = 0x8889000E
	AUDCLNT_E_ENDPOINT_CREATE_FAILED     HRESULT = 0x8889000F
	AUDCLNT_E_SERVICE_NOT_RUNNING        HRESULT = 0x88890010
	AUDCLNT_E_EVENTHANDLE_NOT_EXPECTED   HRESULT = 0x88890011
	AUDCLNT_E_INVALID_DEVICE_PERIOD      HRESULT = 0x88890020
	AUDCLNT_E_RESOURCES_INVALIDATED      HRESULT = 0x88890026

	AUDCLNT_S_BUFFER_EMPTY HRESULT = 0x08890001
)

var hresultNames = map[HRESULT]string{
	E_NOTIMPL:                            "not implemented",
	E_NOINTERFACE:                        "interface not supported",
	E_POINTER:                            "invalid pointer",
	E_FAIL:                               "unspecified failure",
	E_ACCESSDENIED:                       "access denied (check the microphone privacy settings)",
	E_OUTOFMEMORY:                        "out of memory",
	E_INVALIDARG:                         "invalid argument",
	E_NOTFOUND:                           "element not found",
	E_FILE_NOT_FOUND:                     "not found",
	RPC_E_CHANGED_MODE:                   "COM apartment mode already set",
	REGDB_E_CLASSNOTREG:                  "class not registered",
	AUDCLNT_E_NOT_INITIALIZED:            "audio client not initialized",
	AUDCLNT_E_ALREADY_INITIALIZED:        "audio client already initialized",
	AUDCLNT_E_WRONG_ENDPOINT_TYPE:        "wrong endpoint type",
	AUDCLNT_E_DEVICE_INVALIDATED:         "audio device invalidated (unplugged or disabled)",
	AUDCLNT_E_NOT_STOPPED:                "audio stream not stopped",
	AUDCLNT_E_BUFFER_TOO_LARGE:           "buffer too large",
	AUDCLNT_E_OUT_OF_ORDER:               "out of order",
	AUDCLNT_E_UNSUPPORTED_FORMAT:         "unsupported format",
	AUDCLNT_E_INVALID_SIZE:               "invalid size",
	AUDCLNT_E_DEVICE_IN_USE:              "audio device in use",
	AUDCLNT_E_BUFFER_OPERATION_PENDING:   "buffer operation pending",
	AUDCLNT_E_THREAD_NOT_REGISTERED:      "thread not registered",
	AUDCLNT_E_EXCLUSIVE_MODE_NOT_ALLOWED: "exclusive mode not allowed",
	AUDCLNT_E_ENDPOINT_CREATE_FAILED:     "endpoint creation failed",
	AUDCLNT_E_SERVICE_NOT_RUNNING:        "Windows Audio service not running",
	AUDCLNT_E_EVENTHANDLE_NOT_EXPECTED:   "event handle not expected",
	AUDCLNT_E_INVALID_DEVICE_PERIOD:      "invalid device period",
	AUDCLNT_E_RESOURCES_INVALIDATED:      "audio resources invalidated",
}

// hrError converts a failed HRESULT from op into an error. Codes with an
// audio sentinel are returned as a *audio.BackendError wrapping it, so
// errors.Is works while the native code stays visible.
func hrError(op string, hr HRESULT) error {
	var sentinel error
	switch hr {
	case E_ACCESSDENIED:
		sentinel = audio.ErrPermission
	case AUDCLNT_E_DEVICE_IN_USE:
		sentinel = audio.ErrBusy
	case AUDCLNT_E_DEVICE_INVALIDATED, AUDCLNT_E_RESOURCES_INVALIDATED:
		sentinel = audio.ErrClosed
	case AUDCLNT_E_UNSUPPORTED_FORMAT:
		sentinel = audio.ErrUnsupported
	default:
		if name, ok := hresultNames[hr]; ok {
			sentinel = errors.New(name)
		}
	}
	return &audio.BackendError{Backend: backendName, Op: op, Code: int64(uint32(hr)), Err: sentinel}
}

// lookupError maps the result of resolving a device: "not found" codes
// become notFound (audio.ErrNotFound for an explicit ID, audio.ErrNoDevice
// for the default endpoint); everything else goes through hrError.
func lookupError(op string, hr HRESULT, notFound error) error {
	switch hr {
	case E_NOTFOUND, E_FILE_NOT_FOUND, E_INVALIDARG:
		return &audio.BackendError{Backend: backendName, Op: op, Code: int64(uint32(hr)), Err: notFound}
	}
	return hrError(op, hr)
}

// channelMask returns dwChannelMask for a channel count: mono is front
// centre, stereo front left|right, anything else unspecified (0).
func channelMask(channels int) uint32 {
	switch channels {
	case 1:
		return SPEAKER_FRONT_CENTER
	case 2:
		return SPEAKER_FRONT_LEFT | SPEAKER_FRONT_RIGHT
	}
	return 0
}

// waveFormatExtensible encodes the packed WAVEFORMATEXTENSIBLE (mmreg.h,
// pack(1) WAVEFORMATEX header) describing cfg:
//
//	0  wFormatTag      u16  WAVE_FORMAT_EXTENSIBLE
//	2  nChannels       u16
//	4  nSamplesPerSec  u32
//	8  nAvgBytesPerSec u32
//	12 nBlockAlign     u16
//	14 wBitsPerSample  u16
//	16 cbSize          u16  22
//	18 wValidBitsPerSample u16 (Samples union)
//	20 dwChannelMask   u32
//	24 SubFormat       GUID
func waveFormatExtensible(cfg audio.Config) [sizeofWAVEFORMATEXTENSIBLE]byte {
	var b [sizeofWAVEFORMATEXTENSIBLE]byte
	bits := cfg.Format.Size() * 8
	block := cfg.Channels * cfg.Format.Size()
	sub := KSDATAFORMAT_SUBTYPE_PCM
	if cfg.Format == audio.F32 {
		sub = KSDATAFORMAT_SUBTYPE_IEEE_FLOAT
	}
	le := binary.LittleEndian
	le.PutUint16(b[0:], WAVE_FORMAT_EXTENSIBLE)
	le.PutUint16(b[2:], uint16(cfg.Channels))
	le.PutUint32(b[4:], uint32(cfg.SampleRate))
	le.PutUint32(b[8:], uint32(cfg.SampleRate*block))
	le.PutUint16(b[12:], uint16(block))
	le.PutUint16(b[14:], uint16(bits))
	le.PutUint16(b[16:], cbSizeWAVEFORMATEXTENSIBLE)
	le.PutUint16(b[18:], uint16(bits))
	le.PutUint32(b[20:], channelMask(cfg.Channels))
	g := sub.bytes()
	copy(b[24:], g[:])
	return b
}

// parseWaveFormat reads the sample rate and channel count from the header
// of a WAVEFORMATEX (the first 18 bytes suffice; the extensible tail is not
// needed for those two fields).
func parseWaveFormat(b []byte) (sampleRate, channels int, ok bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	channels = int(binary.LittleEndian.Uint16(b[2:]))
	sampleRate = int(binary.LittleEndian.Uint32(b[4:]))
	return sampleRate, channels, true
}

// bufferDuration is the IAudioClient::Initialize buffer duration for cfg
// in REFERENCE_TIME (100 ns) units: four periods, at least 100 ms.
func bufferDuration(cfg audio.Config) int64 {
	d := max(4*cfg.Period, 100*time.Millisecond)
	return int64(d / 100)
}

// ringSize is the number of periods queued for a slow reader: about two
// seconds of audio, at least 4 buffers.
func ringSize(cfg audio.Config) int {
	return max(4, int(2*time.Second/cfg.Period))
}

package sys

import "fmt"

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

// Name returns the description of a known code, or "".
func (hr HRESULT) Name() string { return hresultNames[hr] }

// HRESULTFromWin32 converts a Win32 error code (HRESULT_FROM_WIN32).
func HRESULTFromWin32(code uint32) HRESULT {
	if int32(code) <= 0 {
		return HRESULT(code)
	}
	return HRESULT(code&0xFFFF | 7<<16 | 0x80000000)
}

// Result codes (winerror.h, mferror.h).
const (
	S_OK    HRESULT = 0
	S_FALSE HRESULT = 1

	E_NOTIMPL          HRESULT = 0x80004001
	E_NOINTERFACE      HRESULT = 0x80004002
	E_POINTER          HRESULT = 0x80004003
	E_ABORT            HRESULT = 0x80004004
	E_FAIL             HRESULT = 0x80004005
	E_ACCESSDENIED     HRESULT = 0x80070005
	E_OUTOFMEMORY      HRESULT = 0x8007000E
	E_INVALIDARG       HRESULT = 0x80070057
	RPC_E_CHANGED_MODE HRESULT = 0x80010106

	// HRESULT_FROM_WIN32 of ERROR_SHARING_VIOLATION (32),
	// ERROR_BUSY (170), ERROR_DEVICE_IN_USE (2404),
	// ERROR_DEVICE_NOT_CONNECTED (1167), ERROR_GEN_FAILURE (31) and
	// ERROR_FILE_NOT_FOUND (2).
	HRESULT_SHARING_VIOLATION    HRESULT = 0x80070020
	HRESULT_BUSY                 HRESULT = 0x800700AA
	HRESULT_DEVICE_IN_USE        HRESULT = 0x80070964
	HRESULT_DEVICE_NOT_CONNECTED HRESULT = 0x8007048F
	HRESULT_GEN_FAILURE          HRESULT = 0x8007001F
	HRESULT_FILE_NOT_FOUND       HRESULT = 0x80070002

	MF_E_PLATFORM_NOT_INITIALIZED           HRESULT = 0xC00D36B0
	MF_E_INVALIDREQUEST                     HRESULT = 0xC00D36B2
	MF_E_INVALIDMEDIATYPE                   HRESULT = 0xC00D36B4
	MF_E_NO_MORE_TYPES                      HRESULT = 0xC00D36B9
	MF_E_INVALIDSTREAMNUMBER                HRESULT = 0xC00D36BB
	MF_E_INVALIDTYPE                        HRESULT = 0xC00D36BD
	MF_E_NOT_FOUND                          HRESULT = 0xC00D36D5
	MF_E_NOT_AVAILABLE                      HRESULT = 0xC00D36D6
	MF_E_ATTRIBUTENOTFOUND                  HRESULT = 0xC00D36E6
	MF_E_UNAUTHORIZED                       HRESULT = 0xC00D3701
	MF_E_HW_MFT_FAILED_START_STREAMING      HRESULT = 0xC00D3704
	MF_E_SHUTDOWN                           HRESULT = 0xC00D3E85
	MF_E_UNSUPPORTED_FORMAT                 HRESULT = 0xC00D3E98
	MF_E_VIDEO_DEVICE_LOCKED                HRESULT = 0xC00D4E24
	MF_E_TOPO_CODEC_NOT_FOUND               HRESULT = 0xC00D5212
	MF_E_VIDEO_RECORDING_DEVICE_INVALIDATED HRESULT = 0xC00DABE0
	MF_E_VIDEO_RECORDING_DEVICE_PREEMPTED   HRESULT = 0xC00DABE1
)

var hresultNames = map[HRESULT]string{
	E_NOTIMPL:                               "not implemented",
	E_NOINTERFACE:                           "interface not supported",
	E_POINTER:                               "invalid pointer",
	E_ABORT:                                 "aborted",
	E_FAIL:                                  "unspecified failure",
	E_ACCESSDENIED:                          "access denied (check the camera privacy settings)",
	E_OUTOFMEMORY:                           "out of memory",
	E_INVALIDARG:                            "invalid argument",
	RPC_E_CHANGED_MODE:                      "COM apartment mode already set",
	HRESULT_SHARING_VIOLATION:               "sharing violation",
	HRESULT_BUSY:                            "device busy",
	HRESULT_DEVICE_IN_USE:                   "device in use",
	HRESULT_DEVICE_NOT_CONNECTED:            "device not connected",
	HRESULT_GEN_FAILURE:                     "device not functioning",
	HRESULT_FILE_NOT_FOUND:                  "not found",
	MF_E_PLATFORM_NOT_INITIALIZED:           "Media Foundation is not initialized",
	MF_E_INVALIDREQUEST:                     "invalid request",
	MF_E_INVALIDMEDIATYPE:                   "invalid media type",
	MF_E_NO_MORE_TYPES:                      "no more media types",
	MF_E_INVALIDSTREAMNUMBER:                "invalid stream number",
	MF_E_INVALIDTYPE:                        "invalid type",
	MF_E_NOT_FOUND:                          "not found",
	MF_E_NOT_AVAILABLE:                      "not available",
	MF_E_ATTRIBUTENOTFOUND:                  "attribute not found",
	MF_E_UNAUTHORIZED:                       "unauthorized",
	MF_E_HW_MFT_FAILED_START_STREAMING:      "camera failed to start streaming (in use by another application?)",
	MF_E_SHUTDOWN:                           "object was shut down",
	MF_E_UNSUPPORTED_FORMAT:                 "unsupported format",
	MF_E_VIDEO_DEVICE_LOCKED:                "video device is locked",
	MF_E_TOPO_CODEC_NOT_FOUND:               "no decoder or converter for this format",
	MF_E_VIDEO_RECORDING_DEVICE_INVALIDATED: "camera was taken by another application or removed",
	MF_E_VIDEO_RECORDING_DEVICE_PREEMPTED:   "camera was preempted by another application",
}

//go:build windows && (amd64 || arm64)

package wasapi

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// COM interfaces are called through their vtables. Each Go type below is a
// view of the native object pointer: the first word of the object points at
// the vtable, whose entries are listed in declaration order from the SDK
// headers (inherited methods first). Go pointers are converted to uintptr
// inside the SyscallN call so the pointed-to memory stays alive for it.
//
// Only 64-bit targets are supported: REFERENCE_TIME (int64) arguments are
// passed as one register-sized word.

var (
	modOle32    = windows.NewLazySystemDLL("ole32.dll")
	modMmdevapi = windows.NewLazySystemDLL("mmdevapi.dll")

	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procPropVariantClear = modOle32.NewProc("PropVariantClear")
)

func hres(r uintptr) HRESULT { return HRESULT(uint32(r)) }

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

// iunknown is embedded (as the first field) by every interface view.
type iunknown struct{ vtbl unsafe.Pointer }

func (u *iunknown) unk() *iUnknownVtbl { return (*iUnknownVtbl)(u.vtbl) }

func (u *iunknown) queryInterface(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(u.unk().QueryInterface, uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// Release decrements the reference count; no-op on nil.
func (u *iunknown) Release() {
	if u == nil {
		return
	}
	syscall.SyscallN(u.unk().Release, uintptr(unsafe.Pointer(u)))
}

// IMMDeviceEnumerator (mmdeviceapi.h).
type IMMDeviceEnumerator struct{ iunknown }

type iMMDeviceEnumeratorVtbl struct {
	iUnknownVtbl
	EnumAudioEndpoints                     uintptr
	GetDefaultAudioEndpoint                uintptr
	GetDevice                              uintptr
	RegisterEndpointNotificationCallback   uintptr
	UnregisterEndpointNotificationCallback uintptr
}

func (e *IMMDeviceEnumerator) v() *iMMDeviceEnumeratorVtbl {
	return (*iMMDeviceEnumeratorVtbl)(e.vtbl)
}

func (e *IMMDeviceEnumerator) EnumAudioEndpoints(flow, stateMask uint32, out **IMMDeviceCollection) HRESULT {
	r, _, _ := syscall.SyscallN(e.v().EnumAudioEndpoints, uintptr(unsafe.Pointer(e)), uintptr(flow), uintptr(stateMask), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

func (e *IMMDeviceEnumerator) GetDefaultAudioEndpoint(flow, role uint32, out **IMMDevice) HRESULT {
	r, _, _ := syscall.SyscallN(e.v().GetDefaultAudioEndpoint, uintptr(unsafe.Pointer(e)), uintptr(flow), uintptr(role), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

func (e *IMMDeviceEnumerator) GetDevice(id *uint16, out **IMMDevice) HRESULT {
	r, _, _ := syscall.SyscallN(e.v().GetDevice, uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// IMMDeviceCollection (mmdeviceapi.h).
type IMMDeviceCollection struct{ iunknown }

type iMMDeviceCollectionVtbl struct {
	iUnknownVtbl
	GetCount uintptr
	Item     uintptr
}

func (c *IMMDeviceCollection) v() *iMMDeviceCollectionVtbl {
	return (*iMMDeviceCollectionVtbl)(c.vtbl)
}

func (c *IMMDeviceCollection) GetCount(n *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().GetCount, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(n)))
	return hres(r)
}

func (c *IMMDeviceCollection) Item(i uint32, out **IMMDevice) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().Item, uintptr(unsafe.Pointer(c)), uintptr(i), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// IMMDevice (mmdeviceapi.h).
type IMMDevice struct{ iunknown }

type iMMDeviceVtbl struct {
	iUnknownVtbl
	Activate          uintptr
	OpenPropertyStore uintptr
	GetId             uintptr
	GetState          uintptr
}

func (d *IMMDevice) v() *iMMDeviceVtbl { return (*iMMDeviceVtbl)(d.vtbl) }

// Activate creates the interface iid on the device (activation params nil).
func (d *IMMDevice) Activate(iid *GUID, clsctx uint32, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(d.v().Activate, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(iid)), uintptr(clsctx), 0, uintptr(out))
	return hres(r)
}

func (d *IMMDevice) OpenPropertyStore(access uint32, out **IPropertyStore) HRESULT {
	r, _, _ := syscall.SyscallN(d.v().OpenPropertyStore, uintptr(unsafe.Pointer(d)), uintptr(access), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetId stores a CoTaskMemAlloc'ed endpoint ID string in *out.
func (d *IMMDevice) GetId(out **uint16) HRESULT {
	r, _, _ := syscall.SyscallN(d.v().GetId, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

func (d *IMMDevice) GetState(state *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(d.v().GetState, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(state)))
	return hres(r)
}

// IMMEndpoint (mmdeviceapi.h), queried from an IMMDevice.
type IMMEndpoint struct{ iunknown }

type iMMEndpointVtbl struct {
	iUnknownVtbl
	GetDataFlow uintptr
}

func (e *IMMEndpoint) GetDataFlow(flow *uint32) HRESULT {
	r, _, _ := syscall.SyscallN((*iMMEndpointVtbl)(e.vtbl).GetDataFlow, uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(flow)))
	return hres(r)
}

// IPropertyStore (propsys.h).
type IPropertyStore struct{ iunknown }

type iPropertyStoreVtbl struct {
	iUnknownVtbl
	GetCount uintptr
	GetAt    uintptr
	GetValue uintptr
	SetValue uintptr
	Commit   uintptr
}

func (p *IPropertyStore) GetValue(key *PROPERTYKEY, pv *PROPVARIANT) HRESULT {
	r, _, _ := syscall.SyscallN((*iPropertyStoreVtbl)(p.vtbl).GetValue, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(pv)))
	return hres(r)
}

// PROPVARIANT is the 64-bit layout (propidl.h): 24 bytes.
type PROPVARIANT struct {
	VT        uint16
	reserved1 uint16
	reserved2 uint16
	reserved3 uint16
	Val       unsafe.Pointer // first word of the union (pwszVal for VT_LPWSTR)
	_         uintptr
}

// Clear frees the value (PropVariantClear).
func (pv *PROPVARIANT) Clear() {
	procPropVariantClear.Call(uintptr(unsafe.Pointer(pv)))
}

// IAudioClient (audioclient.h).
type IAudioClient struct{ iunknown }

type iAudioClientVtbl struct {
	iUnknownVtbl
	Initialize        uintptr
	GetBufferSize     uintptr
	GetStreamLatency  uintptr
	GetCurrentPadding uintptr
	IsFormatSupported uintptr
	GetMixFormat      uintptr
	GetDevicePeriod   uintptr
	Start             uintptr
	Stop              uintptr
	Reset             uintptr
	SetEventHandle    uintptr
	GetService        uintptr
}

func (c *IAudioClient) v() *iAudioClientVtbl { return (*iAudioClientVtbl)(c.vtbl) }

func (c *IAudioClient) Initialize(shareMode, streamFlags uint32, bufferDuration, periodicity int64, format unsafe.Pointer, session *GUID) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().Initialize, uintptr(unsafe.Pointer(c)), uintptr(shareMode), uintptr(streamFlags),
		uintptr(bufferDuration), uintptr(periodicity), uintptr(format), uintptr(unsafe.Pointer(session)))
	return hres(r)
}

// GetMixFormat stores a CoTaskMemAlloc'ed WAVEFORMATEX in *out.
func (c *IAudioClient) GetMixFormat(out *unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().GetMixFormat, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

func (c *IAudioClient) Start() HRESULT {
	r, _, _ := syscall.SyscallN(c.v().Start, uintptr(unsafe.Pointer(c)))
	return hres(r)
}

func (c *IAudioClient) Stop() HRESULT {
	r, _, _ := syscall.SyscallN(c.v().Stop, uintptr(unsafe.Pointer(c)))
	return hres(r)
}

func (c *IAudioClient) SetEventHandle(h windows.Handle) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().SetEventHandle, uintptr(unsafe.Pointer(c)), uintptr(h))
	return hres(r)
}

func (c *IAudioClient) GetService(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().GetService, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// IAudioCaptureClient (audioclient.h).
type IAudioCaptureClient struct{ iunknown }

type iAudioCaptureClientVtbl struct {
	iUnknownVtbl
	GetBuffer         uintptr
	ReleaseBuffer     uintptr
	GetNextPacketSize uintptr
}

func (c *IAudioCaptureClient) v() *iAudioCaptureClientVtbl {
	return (*iAudioCaptureClientVtbl)(c.vtbl)
}

func (c *IAudioCaptureClient) GetBuffer(data **byte, frames, flags *uint32, devicePos, qpcPos *uint64) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().GetBuffer, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(data)),
		uintptr(unsafe.Pointer(frames)), uintptr(unsafe.Pointer(flags)), uintptr(unsafe.Pointer(devicePos)), uintptr(unsafe.Pointer(qpcPos)))
	return hres(r)
}

func (c *IAudioCaptureClient) ReleaseBuffer(frames uint32) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().ReleaseBuffer, uintptr(unsafe.Pointer(c)), uintptr(frames))
	return hres(r)
}

func (c *IAudioCaptureClient) GetNextPacketSize(n *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(c.v().GetNextPacketSize, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(n)))
	return hres(r)
}

// coCreateInstance wraps CoCreateInstance(clsid, nil, clsctx, iid, out).
func coCreateInstance(clsid *GUID, clsctx uint32, iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, uintptr(clsctx), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// Compile-time layout checks: each array length is negative (a compile
// error) unless the size matches the SDK header exactly.
const ptr = unsafe.Sizeof(uintptr(0))

var (
	_ [3*ptr - unsafe.Sizeof(iUnknownVtbl{})]byte
	_ [unsafe.Sizeof(iUnknownVtbl{}) - 3*ptr]byte
	_ [8*ptr - unsafe.Sizeof(iMMDeviceEnumeratorVtbl{})]byte
	_ [unsafe.Sizeof(iMMDeviceEnumeratorVtbl{}) - 8*ptr]byte
	_ [5*ptr - unsafe.Sizeof(iMMDeviceCollectionVtbl{})]byte
	_ [unsafe.Sizeof(iMMDeviceCollectionVtbl{}) - 5*ptr]byte
	_ [7*ptr - unsafe.Sizeof(iMMDeviceVtbl{})]byte
	_ [unsafe.Sizeof(iMMDeviceVtbl{}) - 7*ptr]byte
	_ [4*ptr - unsafe.Sizeof(iMMEndpointVtbl{})]byte
	_ [unsafe.Sizeof(iMMEndpointVtbl{}) - 4*ptr]byte
	_ [8*ptr - unsafe.Sizeof(iPropertyStoreVtbl{})]byte
	_ [unsafe.Sizeof(iPropertyStoreVtbl{}) - 8*ptr]byte
	_ [15*ptr - unsafe.Sizeof(iAudioClientVtbl{})]byte
	_ [unsafe.Sizeof(iAudioClientVtbl{}) - 15*ptr]byte
	_ [6*ptr - unsafe.Sizeof(iAudioCaptureClientVtbl{})]byte
	_ [unsafe.Sizeof(iAudioCaptureClientVtbl{}) - 6*ptr]byte

	_ [sizeofPROPVARIANT64 - unsafe.Sizeof(PROPVARIANT{})]byte
	_ [unsafe.Sizeof(PROPVARIANT{}) - sizeofPROPVARIANT64]byte
	_ [20 - unsafe.Sizeof(PROPERTYKEY{})]byte
	_ [unsafe.Sizeof(PROPERTYKEY{}) - 20]byte
	_ [16 - unsafe.Sizeof(GUID{})]byte
	_ [unsafe.Sizeof(GUID{}) - 16]byte
	_ [8 - unsafe.Offsetof(PROPVARIANT{}.Val)]byte
	_ [unsafe.Offsetof(PROPVARIANT{}.Val) - 8]byte
)

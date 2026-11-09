//go:build windows && (amd64 || arm64)

package sys

import (
	"syscall"
	"unsafe"
)

// COM interfaces are called through their vtables. Each Go type below is a
// view of the native object pointer: the first word of the object points at
// the vtable, whose entries are listed in declaration order from the SDK
// headers (inherited methods first). Methods convert Go pointers to uintptr
// inside the SyscallN call so the compiler keeps the pointed-to memory alive
// for the duration of the call.

func hres(r uintptr) HRESULT { return HRESULT(uint32(r)) }

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// IUnknown is the root COM interface (unknwn.h).
type IUnknown struct{ vtbl *iUnknownVtbl }

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

// QueryInterface stores an interface pointer for iid in *out.
func (u *IUnknown) QueryInterface(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(u.vtbl.QueryInterface, uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// AddRef increments the reference count.
func (u *IUnknown) AddRef() uint32 {
	r, _, _ := syscall.SyscallN(u.vtbl.AddRef, uintptr(unsafe.Pointer(u)))
	return uint32(r)
}

// Release decrements the reference count. It is a no-op on a nil receiver.
func (u *IUnknown) Release() uint32 {
	if u == nil {
		return 0
	}
	r, _, _ := syscall.SyscallN(u.vtbl.Release, uintptr(unsafe.Pointer(u)))
	return uint32(r)
}

// IMFAttributes is the Media Foundation attribute store (mfobjects.h).
type IMFAttributes struct{ vtbl *iMFAttributesVtbl }

type iMFAttributesVtbl struct {
	iUnknownVtbl
	GetItem            uintptr
	GetItemType        uintptr
	CompareItem        uintptr
	Compare            uintptr
	GetUINT32          uintptr
	GetUINT64          uintptr
	GetDouble          uintptr
	GetGUID            uintptr
	GetStringLength    uintptr
	GetString          uintptr
	GetAllocatedString uintptr
	GetBlobSize        uintptr
	GetBlob            uintptr
	GetAllocatedBlob   uintptr
	GetUnknown         uintptr
	SetItem            uintptr
	DeleteItem         uintptr
	DeleteAllItems     uintptr
	SetUINT32          uintptr
	SetUINT64          uintptr
	SetDouble          uintptr
	SetGUID            uintptr
	SetString          uintptr
	SetBlob            uintptr
	SetUnknown         uintptr
	LockStore          uintptr
	UnlockStore        uintptr
	GetCount           uintptr
	GetItemByIndex     uintptr
	CopyAllItems       uintptr
}

// Unknown returns the IUnknown view.
func (a *IMFAttributes) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(a)) }

// Release releases the object. It is a no-op on a nil receiver.
func (a *IMFAttributes) Release() {
	if a != nil {
		a.Unknown().Release()
	}
}

// GetUINT32 reads a UINT32 attribute.
func (a *IMFAttributes) GetUINT32(key *GUID, out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetUINT32, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetUINT64 reads a UINT64 attribute.
func (a *IMFAttributes) GetUINT64(key *GUID, out *uint64) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetUINT64, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetGUID reads a GUID attribute.
func (a *IMFAttributes) GetGUID(key *GUID, out *GUID) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetGUID, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetBlobSize reads the size of a blob attribute.
func (a *IMFAttributes) GetBlobSize(key *GUID, out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetBlobSize, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetBlob copies a blob attribute into buf.
func (a *IMFAttributes) GetBlob(key *GUID, buf *byte, size uint32, written *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetBlob, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(buf)), uintptr(size), uintptr(unsafe.Pointer(written)))
	return hres(r)
}

// GetAllocatedString reads a string attribute into COM-allocated memory,
// which the caller frees with CoTaskMemFree.
func (a *IMFAttributes) GetAllocatedString(key *GUID, out **uint16, length *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetAllocatedString, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)), uintptr(unsafe.Pointer(length)))
	return hres(r)
}

// SetUINT32 writes a UINT32 attribute.
func (a *IMFAttributes) SetUINT32(key *GUID, v uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.SetUINT32, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(v))
	return hres(r)
}

// SetUINT64 writes a UINT64 attribute.
func (a *IMFAttributes) SetUINT64(key *GUID, v uint64) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.SetUINT64, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(v))
	return hres(r)
}

// SetGUID writes a GUID attribute.
func (a *IMFAttributes) SetGUID(key *GUID, v *GUID) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.SetGUID, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(v)))
	return hres(r)
}

// UINT32 returns a UINT32 attribute and whether it is present.
func (a *IMFAttributes) UINT32(key *GUID) (uint32, bool) {
	var v uint32
	if a.GetUINT32(key, &v).Failed() {
		return 0, false
	}
	return v, true
}

// UINT64 returns a UINT64 attribute and whether it is present.
func (a *IMFAttributes) UINT64(key *GUID) (uint64, bool) {
	var v uint64
	if a.GetUINT64(key, &v).Failed() {
		return 0, false
	}
	return v, true
}

// GUID returns a GUID attribute and whether it is present.
func (a *IMFAttributes) GUID(key *GUID) (GUID, bool) {
	var v GUID
	if a.GetGUID(key, &v).Failed() {
		return GUID{}, false
	}
	return v, true
}

// Blob returns a copy of a blob attribute and whether it is present.
func (a *IMFAttributes) Blob(key *GUID) ([]byte, bool) {
	var size uint32
	if a.GetBlobSize(key, &size).Failed() || size == 0 {
		return nil, false
	}
	buf := make([]byte, size)
	var written uint32
	if a.GetBlob(key, &buf[0], size, &written).Failed() {
		return nil, false
	}
	return buf[:min(written, size)], true
}

// String returns a string attribute and whether it is present.
func (a *IMFAttributes) String(key *GUID) (string, bool) {
	var p *uint16
	var n uint32
	if a.GetAllocatedString(key, &p, &n).Failed() || p == nil {
		return "", false
	}
	s := UTF16PtrToString(p)
	CoTaskMemFree(unsafe.Pointer(p))
	return s, true
}

// IMFMediaType describes a media format (mfobjects.h).
type IMFMediaType struct{ vtbl *iMFMediaTypeVtbl }

type iMFMediaTypeVtbl struct {
	iMFAttributesVtbl
	GetMajorType       uintptr
	IsCompressedFormat uintptr
	IsEqual            uintptr
	GetRepresentation  uintptr
	FreeRepresentation uintptr
}

// Attributes returns the IMFAttributes view.
func (t *IMFMediaType) Attributes() *IMFAttributes { return (*IMFAttributes)(unsafe.Pointer(t)) }

// Release releases the object. It is a no-op on a nil receiver.
func (t *IMFMediaType) Release() {
	if t != nil {
		t.Attributes().Release()
	}
}

// IMFActivate creates an object on demand (mfobjects.h).
type IMFActivate struct{ vtbl *iMFActivateVtbl }

type iMFActivateVtbl struct {
	iMFAttributesVtbl
	ActivateObject uintptr
	ShutdownObject uintptr
	DetachObject   uintptr
}

// Attributes returns the IMFAttributes view.
func (a *IMFActivate) Attributes() *IMFAttributes { return (*IMFAttributes)(unsafe.Pointer(a)) }

// Release releases the activation object. It is a no-op on a nil receiver.
func (a *IMFActivate) Release() {
	if a != nil {
		a.Attributes().Release()
	}
}

// ActivateObject creates the object and stores an interface pointer in *out.
func (a *IMFActivate) ActivateObject(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.ActivateObject, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// ShutdownObject shuts down the created object.
func (a *IMFActivate) ShutdownObject() HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.ShutdownObject, uintptr(unsafe.Pointer(a)))
	return hres(r)
}

// IMFSample carries media buffers plus timing (mfobjects.h).
type IMFSample struct{ vtbl *iMFSampleVtbl }

type iMFSampleVtbl struct {
	iMFAttributesVtbl
	GetSampleFlags            uintptr
	SetSampleFlags            uintptr
	GetSampleTime             uintptr
	SetSampleTime             uintptr
	GetSampleDuration         uintptr
	SetSampleDuration         uintptr
	GetBufferCount            uintptr
	GetBufferByIndex          uintptr
	ConvertToContiguousBuffer uintptr
	AddBuffer                 uintptr
	RemoveBufferByIndex       uintptr
	RemoveAllBuffers          uintptr
	GetTotalLength            uintptr
	CopyToBuffer              uintptr
}

// Attributes returns the IMFAttributes view.
func (s *IMFSample) Attributes() *IMFAttributes { return (*IMFAttributes)(unsafe.Pointer(s)) }

// Release releases the object. It is a no-op on a nil receiver.
func (s *IMFSample) Release() {
	if s != nil {
		s.Attributes().Release()
	}
}

// GetSampleTime reads the presentation time in 100-nanosecond units.
func (s *IMFSample) GetSampleTime(out *int64) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.GetSampleTime, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetSampleDuration reads the duration in 100-nanosecond units.
func (s *IMFSample) GetSampleDuration(out *int64) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.GetSampleDuration, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetBufferCount returns the number of buffers in the sample.
func (s *IMFSample) GetBufferCount(out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.GetBufferCount, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetBufferByIndex returns a buffer of the sample.
func (s *IMFSample) GetBufferByIndex(index uint32, out **IMFMediaBuffer) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.GetBufferByIndex, uintptr(unsafe.Pointer(s)), uintptr(index), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// ConvertToContiguousBuffer returns the sample data as one buffer (the
// sample's only buffer, AddRef'ed, when it has just one).
func (s *IMFSample) ConvertToContiguousBuffer(out **IMFMediaBuffer) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.ConvertToContiguousBuffer, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// IMFMediaBuffer is a block of memory (mfobjects.h).
type IMFMediaBuffer struct{ vtbl *iMFMediaBufferVtbl }

type iMFMediaBufferVtbl struct {
	iUnknownVtbl
	Lock             uintptr
	Unlock           uintptr
	GetCurrentLength uintptr
	SetCurrentLength uintptr
	GetMaxLength     uintptr
}

// Unknown returns the IUnknown view.
func (b *IMFMediaBuffer) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(b)) }

// Release releases the object. It is a no-op on a nil receiver.
func (b *IMFMediaBuffer) Release() {
	if b != nil {
		b.Unknown().Release()
	}
}

// Lock maps the buffer memory.
func (b *IMFMediaBuffer) Lock(data **byte, maxLength, currentLength *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Lock, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(data)), uintptr(unsafe.Pointer(maxLength)), uintptr(unsafe.Pointer(currentLength)))
	return hres(r)
}

// Unlock unmaps the buffer memory.
func (b *IMFMediaBuffer) Unlock() HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Unlock, uintptr(unsafe.Pointer(b)))
	return hres(r)
}

// GetCurrentLength returns the number of valid bytes.
func (b *IMFMediaBuffer) GetCurrentLength(out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.GetCurrentLength, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// IMF2DBuffer exposes a buffer as a 2D image (mfobjects.h).
type IMF2DBuffer struct{ vtbl *iMF2DBufferVtbl }

type iMF2DBufferVtbl struct {
	iUnknownVtbl
	Lock2D               uintptr
	Unlock2D             uintptr
	GetScanline0AndPitch uintptr
	IsContiguousFormat   uintptr
	GetContiguousLength  uintptr
	ContiguousCopyTo     uintptr
	ContiguousCopyFrom   uintptr
}

// Unknown returns the IUnknown view.
func (b *IMF2DBuffer) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(b)) }

// Release releases the object. It is a no-op on a nil receiver.
func (b *IMF2DBuffer) Release() {
	if b != nil {
		b.Unknown().Release()
	}
}

// Lock2D maps the image: *scanline0 is the first row in display order and
// *pitch the byte distance between rows (negative for bottom-up images).
func (b *IMF2DBuffer) Lock2D(scanline0 **byte, pitch *int32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Lock2D, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(scanline0)), uintptr(unsafe.Pointer(pitch)))
	return hres(r)
}

// Unlock2D unmaps the image.
func (b *IMF2DBuffer) Unlock2D() HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Unlock2D, uintptr(unsafe.Pointer(b)))
	return hres(r)
}

// IMF2DBuffer2 extends IMF2DBuffer with Lock2DSize (mfobjects.h, Windows 8+).
type IMF2DBuffer2 struct{ vtbl *iMF2DBuffer2Vtbl }

type iMF2DBuffer2Vtbl struct {
	iMF2DBufferVtbl
	Lock2DSize uintptr
	Copy2DTo   uintptr
}

// Unknown returns the IUnknown view.
func (b *IMF2DBuffer2) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(b)) }

// Release releases the object. It is a no-op on a nil receiver.
func (b *IMF2DBuffer2) Release() {
	if b != nil {
		b.Unknown().Release()
	}
}

// Lock2DSize maps the image and reports its pitch and the extent of the
// whole mapped buffer.
func (b *IMF2DBuffer2) Lock2DSize(flags uint32, scanline0 **byte, pitch *int32, bufferStart **byte, bufferLength *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Lock2DSize, uintptr(unsafe.Pointer(b)), uintptr(flags), uintptr(unsafe.Pointer(scanline0)), uintptr(unsafe.Pointer(pitch)), uintptr(unsafe.Pointer(bufferStart)), uintptr(unsafe.Pointer(bufferLength)))
	return hres(r)
}

// Unlock2D unmaps the image.
func (b *IMF2DBuffer2) Unlock2D() HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Unlock2D, uintptr(unsafe.Pointer(b)))
	return hres(r)
}

// IMFMediaSource is a media source (mfidl.h). It inherits
// IMFMediaEventGenerator (mfobjects.h).
type IMFMediaSource struct{ vtbl *iMFMediaSourceVtbl }

type iMFMediaSourceVtbl struct {
	iUnknownVtbl
	// IMFMediaEventGenerator
	GetEvent      uintptr
	BeginGetEvent uintptr
	EndGetEvent   uintptr
	QueueEvent    uintptr
	// IMFMediaSource
	GetCharacteristics           uintptr
	CreatePresentationDescriptor uintptr
	Start                        uintptr
	Pause                        uintptr
	Stop                         uintptr
	Shutdown                     uintptr
}

// Unknown returns the IUnknown view.
func (s *IMFMediaSource) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(s)) }

// Release releases the object. It is a no-op on a nil receiver.
func (s *IMFMediaSource) Release() {
	if s != nil {
		s.Unknown().Release()
	}
}

// Shutdown shuts the source down, releasing the device.
func (s *IMFMediaSource) Shutdown() HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.Shutdown, uintptr(unsafe.Pointer(s)))
	return hres(r)
}

// IMFSourceReader reads samples from a media source (mfreadwrite.h).
type IMFSourceReader struct{ vtbl *iMFSourceReaderVtbl }

type iMFSourceReaderVtbl struct {
	iUnknownVtbl
	GetStreamSelection       uintptr
	SetStreamSelection       uintptr
	GetNativeMediaType       uintptr
	GetCurrentMediaType      uintptr
	SetCurrentMediaType      uintptr
	SetCurrentPosition       uintptr
	ReadSample               uintptr
	Flush                    uintptr
	GetServiceForStream      uintptr
	GetPresentationAttribute uintptr
}

// Unknown returns the IUnknown view.
func (r *IMFSourceReader) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(r)) }

// Release releases the object. It is a no-op on a nil receiver.
func (r *IMFSourceReader) Release() {
	if r != nil {
		r.Unknown().Release()
	}
}

// SetStreamSelection selects or deselects a stream.
func (r *IMFSourceReader) SetStreamSelection(stream uint32, selected bool) HRESULT {
	h, _, _ := syscall.SyscallN(r.vtbl.SetStreamSelection, uintptr(unsafe.Pointer(r)), uintptr(stream), boolArg(selected))
	return hres(h)
}

// GetNativeMediaType returns the index'th format the device offers.
func (r *IMFSourceReader) GetNativeMediaType(stream, index uint32, out **IMFMediaType) HRESULT {
	h, _, _ := syscall.SyscallN(r.vtbl.GetNativeMediaType, uintptr(unsafe.Pointer(r)), uintptr(stream), uintptr(index), uintptr(unsafe.Pointer(out)))
	return hres(h)
}

// GetCurrentMediaType returns the format samples are delivered in.
func (r *IMFSourceReader) GetCurrentMediaType(stream uint32, out **IMFMediaType) HRESULT {
	h, _, _ := syscall.SyscallN(r.vtbl.GetCurrentMediaType, uintptr(unsafe.Pointer(r)), uintptr(stream), uintptr(unsafe.Pointer(out)))
	return hres(h)
}

// SetCurrentMediaType sets the output format (a native type also selects
// the device format; a partial type asks for a decoder or converter).
func (r *IMFSourceReader) SetCurrentMediaType(stream uint32, mt *IMFMediaType) HRESULT {
	h, _, _ := syscall.SyscallN(r.vtbl.SetCurrentMediaType, uintptr(unsafe.Pointer(r)), uintptr(stream), 0, uintptr(unsafe.Pointer(mt)))
	return hres(h)
}

// ReadSample reads the next sample synchronously. *sample may be nil (for
// stream ticks and events) even when the call succeeds.
func (r *IMFSourceReader) ReadSample(stream, control uint32, actualStream, flags *uint32, timestamp *int64, sample **IMFSample) HRESULT {
	h, _, _ := syscall.SyscallN(r.vtbl.ReadSample, uintptr(unsafe.Pointer(r)), uintptr(stream), uintptr(control),
		uintptr(unsafe.Pointer(actualStream)), uintptr(unsafe.Pointer(flags)), uintptr(unsafe.Pointer(timestamp)), uintptr(unsafe.Pointer(sample)))
	return hres(h)
}

// Flush drops pending samples of a stream.
func (r *IMFSourceReader) Flush(stream uint32) HRESULT {
	h, _, _ := syscall.SyscallN(r.vtbl.Flush, uintptr(unsafe.Pointer(r)), uintptr(stream))
	return hres(h)
}

// Compile-time checks of the vtable lengths (number of methods including
// the inherited ones) against the SDK headers.
const ptr = unsafe.Sizeof(uintptr(0))

var (
	_ [3*ptr - unsafe.Sizeof(iUnknownVtbl{})]byte
	_ [unsafe.Sizeof(iUnknownVtbl{}) - 3*ptr]byte
	_ [33*ptr - unsafe.Sizeof(iMFAttributesVtbl{})]byte
	_ [unsafe.Sizeof(iMFAttributesVtbl{}) - 33*ptr]byte
	_ [38*ptr - unsafe.Sizeof(iMFMediaTypeVtbl{})]byte
	_ [unsafe.Sizeof(iMFMediaTypeVtbl{}) - 38*ptr]byte
	_ [36*ptr - unsafe.Sizeof(iMFActivateVtbl{})]byte
	_ [unsafe.Sizeof(iMFActivateVtbl{}) - 36*ptr]byte
	_ [47*ptr - unsafe.Sizeof(iMFSampleVtbl{})]byte
	_ [unsafe.Sizeof(iMFSampleVtbl{}) - 47*ptr]byte
	_ [8*ptr - unsafe.Sizeof(iMFMediaBufferVtbl{})]byte
	_ [unsafe.Sizeof(iMFMediaBufferVtbl{}) - 8*ptr]byte
	_ [10*ptr - unsafe.Sizeof(iMF2DBufferVtbl{})]byte
	_ [unsafe.Sizeof(iMF2DBufferVtbl{}) - 10*ptr]byte
	_ [12*ptr - unsafe.Sizeof(iMF2DBuffer2Vtbl{})]byte
	_ [unsafe.Sizeof(iMF2DBuffer2Vtbl{}) - 12*ptr]byte
	_ [13*ptr - unsafe.Sizeof(iMFMediaSourceVtbl{})]byte
	_ [unsafe.Sizeof(iMFMediaSourceVtbl{}) - 13*ptr]byte
	_ [13*ptr - unsafe.Sizeof(iMFSourceReaderVtbl{})]byte
	_ [unsafe.Sizeof(iMFSourceReaderVtbl{}) - 13*ptr]byte
	_ [16 - unsafe.Sizeof(GUID{})]byte
	_ [unsafe.Sizeof(GUID{}) - 16]byte
	_ [20 - unsafe.Sizeof(DEVPROPKEY{})]byte
	_ [unsafe.Sizeof(DEVPROPKEY{}) - 20]byte
)

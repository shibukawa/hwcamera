//go:build windows && (amd64 || arm64)

package sys

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modMfplat      = windows.NewLazySystemDLL("mfplat.dll")
	modMf          = windows.NewLazySystemDLL("mf.dll")
	modMfreadwrite = windows.NewLazySystemDLL("mfreadwrite.dll")
	modCfgmgr32    = windows.NewLazySystemDLL("cfgmgr32.dll")

	procMFStartup                           = modMfplat.NewProc("MFStartup")
	procMFShutdown                          = modMfplat.NewProc("MFShutdown")
	procMFCreateAttributes                  = modMfplat.NewProc("MFCreateAttributes")
	procMFCreateMediaType                   = modMfplat.NewProc("MFCreateMediaType")
	procMFEnumDeviceSources                 = modMf.NewProc("MFEnumDeviceSources")
	procMFCreateSourceReaderFromMediaSource = modMfreadwrite.NewProc("MFCreateSourceReaderFromMediaSource")

	procCM_Get_Device_Interface_PropertyW = modCfgmgr32.NewProc("CM_Get_Device_Interface_PropertyW")
	procCM_Locate_DevNodeW                = modCfgmgr32.NewProc("CM_Locate_DevNodeW")
	procCM_Get_DevNode_PropertyW          = modCfgmgr32.NewProc("CM_Get_DevNode_PropertyW")
	procCM_Get_Parent                     = modCfgmgr32.NewProc("CM_Get_Parent")
)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load loads the Media Foundation DLLs and resolves every entry point the
// backend needs. It does not start Media Foundation (see Startup). The
// result is cached. The configuration-manager functions are optional.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

func load() error {
	for _, m := range []*windows.LazyDLL{modMfplat, modMf, modMfreadwrite} {
		if err := m.Load(); err != nil {
			return fmt.Errorf("mediafoundation: %w", err)
		}
	}
	var missing []string
	for _, p := range []*windows.LazyProc{
		procMFStartup, procMFShutdown, procMFCreateAttributes, procMFCreateMediaType,
		procMFEnumDeviceSources, procMFCreateSourceReaderFromMediaSource,
	} {
		if err := p.Find(); err != nil {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) > 0 {
		return errors.New("mediafoundation: missing symbols: " + strings.Join(missing, ", "))
	}
	return nil
}

var (
	startMu  sync.Mutex
	startRef int
)

// Startup starts Media Foundation on the first call; every successful call
// must be paired with Shutdown. Load must have succeeded.
func Startup() HRESULT {
	startMu.Lock()
	defer startMu.Unlock()
	if startRef == 0 {
		r, _, _ := procMFStartup.Call(uintptr(MF_VERSION), uintptr(MFSTARTUP_FULL))
		if hr := hres(r); hr.Failed() {
			return hr
		}
	}
	startRef++
	return S_OK
}

// Shutdown undoes one Startup; Media Foundation is shut down with the last.
func Shutdown() {
	startMu.Lock()
	defer startMu.Unlock()
	if startRef == 0 {
		return
	}
	startRef--
	if startRef == 0 {
		procMFShutdown.Call()
	}
}

// CoInitializeMTA joins the calling (OS-locked) thread to the multithreaded
// COM apartment. uninit reports whether CoUninitialize must be called when
// the thread is done with COM.
func CoInitializeMTA() (uninit bool, err error) {
	e := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	if e == nil {
		return true, nil
	}
	if errno, ok := e.(syscall.Errno); ok {
		switch HRESULT(uint32(errno)) {
		case S_FALSE:
			return true, nil
		case RPC_E_CHANGED_MODE:
			return false, nil
		}
	}
	return false, fmt.Errorf("mediafoundation: CoInitializeEx: %w", e)
}

// CoUninitialize leaves the COM apartment.
func CoUninitialize() { windows.CoUninitialize() }

// CoTaskMemFree frees memory allocated by COM.
func CoTaskMemFree(p unsafe.Pointer) { windows.CoTaskMemFree(p) }

// UTF16PtrToString converts a NUL-terminated UTF-16 string.
func UTF16PtrToString(p *uint16) string { return windows.UTF16PtrToString(p) }

// MFCreateAttributes creates an empty attribute store.
func MFCreateAttributes(out **IMFAttributes, initialSize uint32) HRESULT {
	r, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(out)), uintptr(initialSize))
	return hres(r)
}

// MFCreateMediaType creates an empty media type.
func MFCreateMediaType(out **IMFMediaType) HRESULT {
	r, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// MFEnumDeviceSources enumerates capture devices. *activates receives a
// CoTaskMemAlloc'ed array of count IMFActivate pointers; the caller releases
// each and frees the array with CoTaskMemFree.
func MFEnumDeviceSources(attrs *IMFAttributes, activates ***IMFActivate, count *uint32) HRESULT {
	r, _, _ := procMFEnumDeviceSources.Call(uintptr(unsafe.Pointer(attrs)), uintptr(unsafe.Pointer(activates)), uintptr(unsafe.Pointer(count)))
	return hres(r)
}

// MFCreateSourceReaderFromMediaSource creates a synchronous source reader.
func MFCreateSourceReaderFromMediaSource(source *IMFMediaSource, attrs *IMFAttributes, out **IMFSourceReader) HRESULT {
	r, _, _ := procMFCreateSourceReaderFromMediaSource.Call(uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(attrs)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// DeviceInterfaceProperty reads a property of a device interface (the
// symbolic link of a camera). ok is false when the property or the
// configuration manager is unavailable.
func DeviceInterfaceProperty(symlink string, key *DEVPROPKEY) (typ uint32, data []byte, ok bool) {
	if procCM_Get_Device_Interface_PropertyW.Find() != nil {
		return 0, nil, false
	}
	p, err := windows.UTF16PtrFromString(symlink)
	if err != nil {
		return 0, nil, false
	}
	return getProperty(func(t *uint32, buf *byte, size *uint32) uint32 {
		r, _, _ := procCM_Get_Device_Interface_PropertyW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(key)),
			uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(buf)), uintptr(unsafe.Pointer(size)), 0)
		return uint32(r)
	})
}

// LocateDevNode returns the device node of a device instance id.
func LocateDevNode(instanceID string) (uint32, bool) {
	if procCM_Locate_DevNodeW.Find() != nil {
		return 0, false
	}
	p, err := windows.UTF16PtrFromString(instanceID)
	if err != nil {
		return 0, false
	}
	var inst uint32
	r, _, _ := procCM_Locate_DevNodeW.Call(uintptr(unsafe.Pointer(&inst)), uintptr(unsafe.Pointer(p)), uintptr(CM_LOCATE_DEVNODE_NORMAL))
	return inst, uint32(r) == CR_SUCCESS
}

// DevNodeParent returns the parent device node.
func DevNodeParent(inst uint32) (uint32, bool) {
	if procCM_Get_Parent.Find() != nil {
		return 0, false
	}
	var parent uint32
	r, _, _ := procCM_Get_Parent.Call(uintptr(unsafe.Pointer(&parent)), uintptr(inst), 0)
	return parent, uint32(r) == CR_SUCCESS
}

// DevNodeProperty reads a property of a device node.
func DevNodeProperty(inst uint32, key *DEVPROPKEY) (typ uint32, data []byte, ok bool) {
	if procCM_Get_DevNode_PropertyW.Find() != nil {
		return 0, nil, false
	}
	return getProperty(func(t *uint32, buf *byte, size *uint32) uint32 {
		r, _, _ := procCM_Get_DevNode_PropertyW.Call(uintptr(inst), uintptr(unsafe.Pointer(key)),
			uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(buf)), uintptr(unsafe.Pointer(size)), 0)
		return uint32(r)
	})
}

// getProperty runs the size-query / fetch protocol of the CM_Get_*_Property
// functions.
func getProperty(call func(typ *uint32, buf *byte, size *uint32) uint32) (uint32, []byte, bool) {
	var typ, size uint32
	if cr := call(&typ, nil, &size); cr != CR_BUFFER_SMALL && cr != CR_SUCCESS {
		return 0, nil, false
	}
	if size == 0 {
		return typ, nil, true
	}
	buf := make([]byte, size)
	if cr := call(&typ, &buf[0], &size); cr != CR_SUCCESS {
		return 0, nil, false
	}
	return typ, buf[:min(int(size), len(buf))], true
}

// UTF16BytesToString decodes a NUL-terminated UTF-16LE property value.
func UTF16BytesToString(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return windows.UTF16ToString(u)
}

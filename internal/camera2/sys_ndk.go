//go:build android || hwcamera_typecheck

package camera2

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Bound functions, valid after load returns nil. NDK objects travel as
// uintptr; out-parameters are pointers to Go variables, which the NDK
// writes during the call only.
var (
	// libcamera2ndk.so (API 24)
	ACameraManager_create                   func() uintptr
	ACameraManager_delete                   func(mgr uintptr)
	ACameraManager_getCameraIdList          func(mgr uintptr, list *unsafe.Pointer) int32
	ACameraManager_deleteCameraIdList       func(list unsafe.Pointer)
	ACameraManager_getCameraCharacteristics func(mgr uintptr, id string, meta *uintptr) int32
	ACameraManager_openCamera               func(mgr uintptr, id string, cb *deviceCallbacks, dev *uintptr) int32
	ACameraMetadata_getConstEntry           func(meta uintptr, tag uint32, entry *constEntry) int32
	ACameraMetadata_free                    func(meta uintptr)

	ACameraDevice_close                       func(dev uintptr) int32
	ACameraDevice_createCaptureRequest        func(dev uintptr, template int32, req *uintptr) int32
	ACameraDevice_createCaptureSession        func(dev, container uintptr, cb *sessionCallbacks, session *uintptr) int32
	ACaptureSessionOutputContainer_create     func(container *uintptr) int32
	ACaptureSessionOutputContainer_free       func(container uintptr)
	ACaptureSessionOutputContainer_add        func(container, output uintptr) int32
	ACaptureSessionOutputContainer_remove     func(container, output uintptr) int32
	ACaptureSessionOutput_create              func(window uintptr, output *uintptr) int32
	ACaptureSessionOutput_free                func(output uintptr)
	ACameraOutputTarget_create                func(window uintptr, target *uintptr) int32
	ACameraOutputTarget_free                  func(target uintptr)
	ACaptureRequest_addTarget                 func(req, target uintptr) int32
	ACaptureRequest_removeTarget              func(req, target uintptr) int32
	ACaptureRequest_setEntry_i32              func(req uintptr, tag uint32, count uint32, data *int32) int32
	ACaptureRequest_free                      func(req uintptr)
	ACameraCaptureSession_setRepeatingRequest func(session, callbacks uintptr, n int32, reqs *uintptr, seqID *int32) int32
	ACameraCaptureSession_stopRepeating       func(session uintptr) int32
	ACameraCaptureSession_close               func(session uintptr)

	// libmediandk.so (API 24)
	AImageReader_new              func(w, h, format, maxImages int32, reader *uintptr) int32
	AImageReader_delete           func(reader uintptr)
	AImageReader_getWindow        func(reader uintptr, window *uintptr) int32
	AImageReader_setImageListener func(reader uintptr, l *imageListener) int32
	AImageReader_acquireNextImage func(reader uintptr, image *uintptr) int32
	AImage_delete                 func(image uintptr)
	AImage_getTimestamp           func(image uintptr, ns *int64) int32
	AImage_getWidth               func(image uintptr, w *int32) int32
	AImage_getHeight              func(image uintptr, h *int32) int32
	AImage_getCropRect            func(image uintptr, rect *cropRect) int32
	AImage_getPlaneRowStride      func(image uintptr, idx int32, stride *int32) int32
	AImage_getPlanePixelStride    func(image uintptr, idx int32, stride *int32) int32
	AImage_getPlaneData           func(image uintptr, idx int32, data **byte, length *int32) int32
)

// deviceCallbacks is ACameraDevice_StateCallbacks.
type deviceCallbacks struct {
	Context        uintptr
	OnDisconnected uintptr // void (*)(void* ctx, ACameraDevice*)
	OnError        uintptr // void (*)(void* ctx, ACameraDevice*, int err)
}

// sessionCallbacks is ACameraCaptureSession_stateCallbacks.
type sessionCallbacks struct {
	Context  uintptr
	OnClosed uintptr // void (*)(void* ctx, ACameraCaptureSession*)
	OnReady  uintptr
	OnActive uintptr
}

// imageListener is AImageReader_ImageListener.
type imageListener struct {
	Context          uintptr
	OnImageAvailable uintptr // void (*)(void* ctx, AImageReader*)
}

// cropRect is AImageCropRect.
type cropRect struct{ Left, Top, Right, Bottom int32 }

// C-callable trampolines, created once: purego callbacks cannot be freed.
// Their context argument is a stream handle (see streams), never a Go
// pointer.
var (
	cbImage, cbDisconnected, cbError, cbSessionNop uintptr
)

var (
	loadOnce sync.Once
	loadErr  error
)

func load() error {
	loadOnce.Do(func() { loadErr = doLoad() })
	return loadErr
}

type binder struct {
	handle uintptr
	lib    string
	errs   []string
}

func (b *binder) fn(fptr any, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		b.errs = append(b.errs, b.lib+":"+name)
		return
	}
	purego.RegisterFunc(fptr, addr)
}

func doLoad() error {
	cam, err := purego.Dlopen("libcamera2ndk.so", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("camera2: dlopen libcamera2ndk.so (Android 7.0 / API 24 or later): %w", err)
	}
	media, err := purego.Dlopen("libmediandk.so", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("camera2: dlopen libmediandk.so: %w", err)
	}
	b := &binder{handle: cam, lib: "libcamera2ndk.so"}
	b.fn(&ACameraManager_create, "ACameraManager_create")
	b.fn(&ACameraManager_delete, "ACameraManager_delete")
	b.fn(&ACameraManager_getCameraIdList, "ACameraManager_getCameraIdList")
	b.fn(&ACameraManager_deleteCameraIdList, "ACameraManager_deleteCameraIdList")
	b.fn(&ACameraManager_getCameraCharacteristics, "ACameraManager_getCameraCharacteristics")
	b.fn(&ACameraManager_openCamera, "ACameraManager_openCamera")
	b.fn(&ACameraMetadata_getConstEntry, "ACameraMetadata_getConstEntry")
	b.fn(&ACameraMetadata_free, "ACameraMetadata_free")
	b.fn(&ACameraDevice_close, "ACameraDevice_close")
	b.fn(&ACameraDevice_createCaptureRequest, "ACameraDevice_createCaptureRequest")
	b.fn(&ACameraDevice_createCaptureSession, "ACameraDevice_createCaptureSession")
	b.fn(&ACaptureSessionOutputContainer_create, "ACaptureSessionOutputContainer_create")
	b.fn(&ACaptureSessionOutputContainer_free, "ACaptureSessionOutputContainer_free")
	b.fn(&ACaptureSessionOutputContainer_add, "ACaptureSessionOutputContainer_add")
	b.fn(&ACaptureSessionOutputContainer_remove, "ACaptureSessionOutputContainer_remove")
	b.fn(&ACaptureSessionOutput_create, "ACaptureSessionOutput_create")
	b.fn(&ACaptureSessionOutput_free, "ACaptureSessionOutput_free")
	b.fn(&ACameraOutputTarget_create, "ACameraOutputTarget_create")
	b.fn(&ACameraOutputTarget_free, "ACameraOutputTarget_free")
	b.fn(&ACaptureRequest_addTarget, "ACaptureRequest_addTarget")
	b.fn(&ACaptureRequest_removeTarget, "ACaptureRequest_removeTarget")
	b.fn(&ACaptureRequest_setEntry_i32, "ACaptureRequest_setEntry_i32")
	b.fn(&ACaptureRequest_free, "ACaptureRequest_free")
	b.fn(&ACameraCaptureSession_setRepeatingRequest, "ACameraCaptureSession_setRepeatingRequest")
	b.fn(&ACameraCaptureSession_stopRepeating, "ACameraCaptureSession_stopRepeating")
	b.fn(&ACameraCaptureSession_close, "ACameraCaptureSession_close")
	b.handle, b.lib = media, "libmediandk.so"
	b.fn(&AImageReader_new, "AImageReader_new")
	b.fn(&AImageReader_delete, "AImageReader_delete")
	b.fn(&AImageReader_getWindow, "AImageReader_getWindow")
	b.fn(&AImageReader_setImageListener, "AImageReader_setImageListener")
	b.fn(&AImageReader_acquireNextImage, "AImageReader_acquireNextImage")
	b.fn(&AImage_delete, "AImage_delete")
	b.fn(&AImage_getTimestamp, "AImage_getTimestamp")
	b.fn(&AImage_getWidth, "AImage_getWidth")
	b.fn(&AImage_getHeight, "AImage_getHeight")
	b.fn(&AImage_getCropRect, "AImage_getCropRect")
	b.fn(&AImage_getPlaneRowStride, "AImage_getPlaneRowStride")
	b.fn(&AImage_getPlanePixelStride, "AImage_getPlanePixelStride")
	b.fn(&AImage_getPlaneData, "AImage_getPlaneData")
	if len(b.errs) > 0 {
		return errors.New("camera2: missing " + strings.Join(b.errs, ", ") + " (Android 7.0 / API 24 or later is required)")
	}

	cbImage = purego.NewCallback(func(ctx, reader uintptr) {
		if s := lookup(ctx); s != nil {
			s.onImage()
		}
	})
	cbDisconnected = purego.NewCallback(func(ctx, dev uintptr) {
		if s := lookup(ctx); s != nil {
			s.fail(statusErr("camera device", errCameraDisconnected))
		}
	})
	cbError = purego.NewCallback(func(ctx, dev, code uintptr) {
		if s := lookup(ctx); s != nil {
			s.fail(deviceErr(int32(code)))
		}
	})
	cbSessionNop = purego.NewCallback(func(ctx, session uintptr) {})
	return nil
}

// entry reads a metadata entry; ok is false when the tag is absent.
func entry(meta uintptr, tag uint32) (constEntry, bool) {
	var e constEntry
	if ACameraMetadata_getConstEntry(meta, tag, &e) != statusOK || e.Data == nil {
		return e, false
	}
	return e, true
}

func entryI32(meta uintptr, tag uint32) []int32 {
	e, ok := entry(meta, tag)
	if !ok || e.Type != typeInt32 || e.Count == 0 {
		return nil
	}
	return append([]int32(nil), unsafe.Slice((*int32)(e.Data), e.Count)...)
}

func entryI64(meta uintptr, tag uint32) []int64 {
	e, ok := entry(meta, tag)
	if !ok || e.Type != typeInt64 || e.Count == 0 {
		return nil
	}
	return append([]int64(nil), unsafe.Slice((*int64)(e.Data), e.Count)...)
}

func entryU8(meta uintptr, tag uint32) (uint8, bool) {
	e, ok := entry(meta, tag)
	if !ok || e.Type != typeByte || e.Count == 0 {
		return 0, false
	}
	return *(*uint8)(e.Data), true
}

func gostr(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

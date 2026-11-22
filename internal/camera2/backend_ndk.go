//go:build android || hwcamera_typecheck

package camera2

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// Backend is the NDK Camera2 backend.
type Backend struct{}

func (Backend) Name() string { return Name }

// Available reports whether libcamera2ndk.so and libmediandk.so load
// (Android 7.0 / API 24 and later).
func (Backend) Available() bool { return load() == nil }

// idList is ACameraIdList.
type idList struct {
	N   int32
	IDs unsafe.Pointer // const char**
}

func cameraIDs(mgr uintptr) ([]string, error) {
	var p unsafe.Pointer
	if st := ACameraManager_getCameraIdList(mgr, &p); st != statusOK {
		return nil, statusErr("ACameraManager_getCameraIdList", st)
	}
	if p == nil {
		return nil, nil
	}
	defer ACameraManager_deleteCameraIdList(p)
	l := (*idList)(p)
	if l.N <= 0 || l.IDs == nil {
		return nil, nil
	}
	var ids []string
	for _, s := range unsafe.Slice((*unsafe.Pointer)(l.IDs), l.N) {
		ids = append(ids, gostr(s))
	}
	return ids, nil
}

type characteristics struct {
	info   camera.DeviceInfo
	ranges []fpsRange
}

func describe(mgr uintptr, id string) (characteristics, error) {
	var meta uintptr
	if st := ACameraManager_getCameraCharacteristics(mgr, id, &meta); st != statusOK {
		return characteristics{}, statusErr("ACameraManager_getCameraCharacteristics", st)
	}
	defer ACameraMetadata_free(meta)
	facing, ok := entryU8(meta, tagLensFacing)
	pos := position(int32(facing), ok)
	ranges := entryI32(meta, tagControlAEAvailableTargetFPSRange)
	return characteristics{
		info: camera.DeviceInfo{
			ID: id, Name: deviceName(id, pos), Backend: Name, Position: pos,
			Lens:  lensFor(entryF32(meta, tagLensInfoFocalLengths), entryF32(meta, tagSensorInfoPhysicalSize)),
			Modes: buildModes(entryI32(meta, tagScalerStreamConfigurations), entryI64(meta, tagScalerMinFrameDurations), ranges),
		},
		ranges: fpsRanges(ranges),
	}, nil
}

func newManager() (uintptr, error) {
	if err := load(); err != nil {
		return 0, fmt.Errorf("%w: %v", camera.ErrUnsupported, err)
	}
	mgr := ACameraManager_create()
	if mgr == 0 {
		return 0, &camera.BackendError{Backend: Name, Op: "ACameraManager_create"}
	}
	return mgr, nil
}

// Devices lists the cameras the camera service exposes (on Android 10+
// logical multi-cameras appear once; their physical sensors are not
// listed). Cameras without YUV output (depth sensors) are left out. The
// first back camera is the default. Listing needs no permission.
func (Backend) Devices(ctx context.Context) ([]camera.DeviceInfo, error) {
	mgr, err := newManager()
	if err != nil {
		return nil, err
	}
	defer ACameraManager_delete(mgr)
	ids, err := cameraIDs(mgr)
	if err != nil {
		return nil, err
	}
	var out []camera.DeviceInfo
	for _, id := range ids {
		c, err := describe(mgr, id)
		if err != nil || len(c.info.Modes) == 0 {
			continue
		}
		out = append(out, c.info)
	}
	markDefault(out)
	return out, nil
}

func (Backend) Open(ctx context.Context, id string, cfg camera.Config) (camera.Stream, error) {
	mgr, err := newManager()
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			ACameraManager_delete(mgr)
		}
	}()
	ids, err := cameraIDs(mgr)
	if err != nil {
		return nil, err
	}
	found := false
	for _, x := range ids {
		found = found || x == id
	}
	if !found {
		return nil, fmt.Errorf("%w: %q", camera.ErrNotFound, id)
	}
	c, err := describe(mgr, id)
	if err != nil {
		return nil, err
	}
	mode, err := camera.ChooseMode(c.info.Modes, cfg)
	if err != nil {
		return nil, err
	}
	fps := camera.PickFrameRate(mode, cfg.FrameRate)
	s, err := openStream(mgr, id, mode, fps, c.ranges, cfg.Buffers)
	if err != nil {
		return nil, err
	}
	keep = true // the stream owns the manager now
	return s, nil
}

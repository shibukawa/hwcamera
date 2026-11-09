//go:build darwin

package avfoundation

import (
	"context"
	"errors"
	"fmt"

	"github.com/ebitengine/purego/objc"
	"github.com/shibukawa/hwcamera/internal/camera"
)

// Name is the backend identifier.
const Name = "avfoundation"

// Backend is the AVFoundation backend.
//
// Camera access goes through macOS privacy control (TCC). An app bundle
// must carry NSCameraUsageDescription in its Info.plist, or the process is
// killed when it asks for access. A command-line binary has no Info.plist:
// the permission is the one of the terminal application it runs in
// (Terminal, iTerm, the IDE), which macOS asks for on first use and which
// System Settings > Privacy & Security > Camera can grant later.
//
// iOS and iPadOS use the same backend (GOOS=ios also satisfies the darwin
// build tag; purego needs CGO_ENABLED=1 there, as gomobile and ebitenmobile
// set). The app's Info.plist MUST carry NSCameraUsageDescription: without
// it iOS kills the process the moment the camera is requested. On iOS:
//
//   - Devices lists the physical cameras only: the wide-angle, ultra-wide
//     and telephoto cameras (back and front) and, on iPadOS 17 and later,
//     external USB cameras. The virtual multi-camera devices (dual, dual
//     wide, triple, TrueDepth) are left out because their video is that
//     of a physical camera already listed. Order: back wide, front wide,
//     ultra-wide, telephoto, external; Default marks the system preferred
//     camera, else +defaultDeviceWithMediaType: (the back wide camera).
//   - Frames come in the sensor's native landscape orientation (for the
//     back camera the image is upright with the device held in landscape,
//     home button / USB port on the right). The library neither rotates
//     nor mirrors the connection, so a frame's size is always the Mode's
//     (a rotation would swap it and cost a copy in the capture pipeline).
//     Rotate in the renderer for a portrait UI.
//   - While the capture session is interrupted (the app went to the
//     background, another app took the camera, Split View, system
//     pressure) Read keeps blocking and frames resume when the
//     interruption ends or the app becomes active again. Media services
//     being reset restarts the session; other runtime errors end the
//     stream with an error.
type Backend struct{}

func (Backend) Name() string { return Name }

func (Backend) Available() bool { return load() == nil }

func loadErrWrap(err error) error {
	return &camera.BackendError{Backend: Name, Op: "load", Err: err}
}

func (Backend) Devices(ctx context.Context) ([]camera.DeviceInfo, error) {
	if err := load(); err != nil {
		return nil, loadErrWrap(err)
	}
	var out []camera.DeviceInfo
	withPool(func() {
		def := preferredID()
		listed := false
		for _, d := range discover() {
			info := describe(d)
			info.Default = info.ID == def
			listed = listed || info.Default
			out = append(out, info)
		}
		if !listed {
			// The preferred camera is one Devices does not list (on iOS a
			// virtual multi-camera device such as the triple camera):
			// mark +defaultDeviceWithMediaType: instead.
			if def = defaultID(); def != "" {
				for i := range out {
					out[i].Default = out[i].ID == def
				}
			}
		}
	})
	return out, nil
}

// preferredID returns the unique ID of the camera the system prefers:
// +systemPreferredCamera (macOS 13 / iOS 17; on macOS the camera picked in
// Control Center or the one last used, on iOS the app's
// userPreferredCamera when set), else +defaultDeviceWithMediaType:.
func preferredID() string {
	c := class("AVCaptureDevice")
	if responds(c, selSystemPreferredCamera) {
		if d := c.Send(selSystemPreferredCamera); d != 0 {
			return goString(d.Send(selUniqueID))
		}
	}
	return defaultID()
}

// defaultID returns the unique ID of +defaultDeviceWithMediaType: (on
// iOS the back wide-angle camera).
func defaultID() string {
	d := class("AVCaptureDevice").Send(selDefaultDevice, avMediaTypeVideo)
	if d == 0 {
		return ""
	}
	return goString(d.Send(selUniqueID))
}

// discover lists the video capture devices in the platform's preferred
// order. The discovery session is the supported API since macOS 10.15 and
// iOS 10; +devicesWithMediaType: covers older systems.
func discover() []objc.ID {
	if ds := class("AVCaptureDeviceDiscoverySession"); ds != 0 {
		if types := discoveryTypes(); len(types) > 0 {
			sess := ds.Send(selDiscoverySession, nsArray(types), avMediaTypeVideo, uintptr(avPositionUnspecified))
			if sess != 0 {
				return orderDevices(dedupe(items(sess.Send(selDevices))))
			}
		}
	}
	return orderDevices(dedupe(items(class("AVCaptureDevice").Send(selDevicesWithMediaType, avMediaTypeVideo))))
}

func dedupe(ds []objc.ID) []objc.ID {
	seen := map[string]bool{}
	out := ds[:0]
	for _, d := range ds {
		id := goString(d.Send(selUniqueID))
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, d)
	}
	return out
}

func kindOf(d objc.ID) deviceKind {
	if !responds(d, selDeviceType) {
		return kindUnknown
	}
	t := d.Send(selDeviceType)
	switch {
	case equalStrings(t, avDeviceTypeBuiltInWideAngle), equalStrings(t, avDeviceTypeBuiltInUltraWide),
		equalStrings(t, avDeviceTypeBuiltInTelephoto):
		return kindBuiltIn
	case equalStrings(t, avDeviceTypeExternal), equalStrings(t, avDeviceTypeExternalUnknown),
		equalStrings(t, avDeviceTypeContinuity):
		return kindExternal
	}
	return kindUnknown
}

func describe(d objc.ID) camera.DeviceInfo {
	info := camera.DeviceInfo{
		ID:       goString(d.Send(selUniqueID)),
		Name:     goString(d.Send(selLocalizedName)),
		Backend:  Name,
		Position: positionFor(int(d.Send(selPosition)), kindOf(d)),
		Virtual:  isVirtual(d),
	}
	var modes []camera.Mode
	for _, f := range deviceFormats(d) {
		modes = append(modes, f.mode)
	}
	info.Modes = mergeModes(modes)
	return info
}

// rateRange is one AVFrameRateRange with the CMTimes it was derived from.
type rateRange struct {
	frameRange
	minDur, maxDur cmTime
}

// devFormat is one AVCaptureDeviceFormat. obj is only valid inside the
// autorelease pool that produced it.
type devFormat struct {
	obj     objc.ID
	subtype uint32
	mode    camera.Mode
	ranges  []rateRange
}

func deviceFormats(d objc.ID) []devFormat {
	var out []devFormat
	for _, f := range items(d.Send(selFormats)) {
		desc := f.Send(selFormatDescription)
		if desc == 0 {
			continue
		}
		sub := cmFormatDescriptionGetMediaSubType(uintptr(desc))
		pf := formatFromFourCC(sub)
		if pf == camera.Unknown {
			continue
		}
		dim := cmVideoFormatDescriptionGetDimensions(uintptr(desc))
		df := devFormat{obj: f, subtype: sub, mode: camera.Mode{Format: pf, Width: int(dim.Width), Height: int(dim.Height)}}
		var frs []frameRange
		for _, r := range items(f.Send(selFrameRateRanges)) {
			rr := rateRange{
				frameRange: frameRange{
					MinRate: objc.Send[float64](r, selMinFrameRate),
					MaxRate: objc.Send[float64](r, selMaxFrameRate),
				},
				minDur: objc.Send[cmTime](r, selMinFrameDuration),
				maxDur: objc.Send[cmTime](r, selMaxFrameDuration),
			}
			df.ranges = append(df.ranges, rr)
			frs = append(frs, rr.frameRange)
		}
		df.mode.FrameRates = ratesOf(frs)
		out = append(out, df)
	}
	return out
}

// pickFormat finds the device format behind a merged mode: one whose
// ranges cover rate, preferring the subtype the output will deliver so
// AVFoundation has nothing to convert.
func pickFormat(fs []devFormat, m camera.Mode, rate float64, outCC uint32) (devFormat, rateRange, bool) {
	var best devFormat
	var bestR rateRange
	score := -1
	for _, f := range fs {
		if f.mode.Format != m.Format || f.mode.Width != m.Width || f.mode.Height != m.Height {
			continue
		}
		s := 0
		var r rateRange
		if len(f.ranges) > 0 {
			r = f.ranges[0]
		}
		for _, x := range f.ranges {
			if rate > 0 && rangeCovers(x.frameRange, rate) {
				r, s = x, 2
				break
			}
		}
		if f.subtype == outCC {
			s++
		}
		if s > score {
			best, bestR, score = f, r, s
		}
	}
	return best, bestR, score >= 0
}

// authorize makes sure the process may use the camera, asking the user
// when nobody has decided yet.
func authorize(ctx context.Context) error {
	var status int
	withPool(func() {
		status = int(class("AVCaptureDevice").Send(selAuthorizationStatus, avMediaTypeVideo))
	})
	switch status {
	case authAuthorized:
		return nil
	case authDenied, authRestricted:
		return camera.ErrPermission
	}
	done := make(chan struct{}, 1)
	withPool(func() {
		// The handler runs on an arbitrary queue. Its BOOL argument is not
		// trusted (the upper register bits are unspecified on x86_64);
		// the status is read again instead.
		blk := objc.NewBlock(func(_ objc.Block, _ bool) {
			select {
			case done <- struct{}{}:
			default:
			}
		})
		class("AVCaptureDevice").Send(selRequestAccess, avMediaTypeVideo, blk)
		// AVFoundation copied the block; drop the reference NewBlock gave us.
		blk.Release()
	})
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	withPool(func() {
		status = int(class("AVCaptureDevice").Send(selAuthorizationStatus, avMediaTypeVideo))
	})
	if status != authAuthorized {
		return camera.ErrPermission
	}
	return nil
}

// AVError codes (AVError.h) that map to the package's sentinel errors.
const (
	avErrorDeviceAlreadyUsedByAnotherSession  = -11804
	avErrorDeviceNotConnected                 = -11814
	avErrorApplicationIsNotAuthorizedToUseDev = -11852

	// Not mapped to sentinels; the session is restarted instead.
	avErrorMediaServicesWereReset           = -11819
	avErrorDeviceIsNotAvailableInBackground = -11837
)

func nativeErr(op string, nserr objc.ID) error {
	code, err := nsError(nserr)
	var sentinel error
	switch code {
	case avErrorDeviceAlreadyUsedByAnotherSession:
		sentinel = camera.ErrBusy
	case avErrorDeviceNotConnected:
		sentinel = camera.ErrNotFound
	case avErrorApplicationIsNotAuthorizedToUseDev:
		sentinel = camera.ErrPermission
	}
	switch {
	case sentinel != nil && err != nil:
		err = fmt.Errorf("%w: %v", sentinel, err)
	case sentinel != nil:
		err = sentinel
	case err == nil:
		err = errors.New("failed")
	}
	return &camera.BackendError{Backend: Name, Op: op, Code: code, Err: err}
}

func (Backend) Open(ctx context.Context, id string, cfg camera.Config) (camera.Stream, error) {
	if err := load(); err != nil {
		return nil, loadErrWrap(err)
	}
	if err := registerDelegate(); err != nil {
		return nil, &camera.BackendError{Backend: Name, Op: "register delegate class", Err: err}
	}
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	var s *stream
	var err error
	withPool(func() { s, err = openStream(id, cfg) })
	if err != nil {
		return nil, err
	}
	return s, nil
}

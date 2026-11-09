//go:build ios

package coreaudio

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/shibukawa/hwcamera/internal/audio"
)

// iOS has no Core Audio HAL: devices, routing, permission and
// interruptions go through AVAudioSession (AVFAudio.framework, re-exported
// by AVFoundation). Capture itself is the shared AudioQueue code.

// AVAudioSession and AVAudioApplication constants.
const (
	// AVAudioSessionCategoryOptions.
	avCategoryOptionAllowBluetooth   uintptr = 0x4 // AllowBluetooth (AllowBluetoothHFP in the iOS 26 SDK)
	avCategoryOptionDefaultToSpeaker uintptr = 0x8

	// AVAudioSessionInterruptionType.
	avInterruptionTypeEnded = 0
	avInterruptionTypeBegan = 1
)

// AVAudioSessionErrorCode values (AVAudioSessionTypes.h) that map to the
// package's sentinel errors.
var (
	avSessionErrCannotInterruptOthers = int64(fourCC("!int"))
	avSessionErrInsufficientPriority  = int64(fourCC("!pri"))
	avSessionErrSiriIsRecording       = int64(fourCC("siri"))
	avSessionErrCannotStartRecording  = int64(fourCC("!rec"))
	avSessionErrIsBusy                = int64(fourCC("!act"))
	avSessionErrMissingEntitlement    = int64(fourCC("ent?"))
)

var (
	avCategoryPlayAndRecord objc.ID
	avCategoryRecord        objc.ID
	avModeDefault           objc.ID

	avInterruptionNotification       objc.ID
	avInterruptionTypeKey            objc.ID
	avMediaServicesResetNotification objc.ID // optional
	uiAppDidBecomeActiveNotification objc.ID // optional (UIKit)

	selSharedInstance, selCategory, selSetCategoryModeOptions, selSetActive objc.SEL
	selIsInputAvailable, selAvailableInputs, selPreferredInput              objc.SEL
	selSetPreferredInput, selCurrentRoute, selInputs, selPortName, selUID   objc.SEL
	selChannels, selSampleRate, selRecordPermission                         objc.SEL
	selRequestRecordPermission, selRequestRecordPermissionHandler           objc.SEL
	selRespondsToSelector, selIsEqualToString, selUTF8String                objc.SEL
	selCount, selObjectAtIndex, selCode, selLocalizedDescription            objc.SEL
	selDefaultCenter, selAddObserver, selUserInfo, selObjectForKey          objc.SEL
	selUnsignedIntegerValue, selAlloc, selInit                              objc.SEL
	selOnInterruption, selOnMediaReset, selOnBecameActive                   objc.SEL

	// interrupted is set between the begin and end of an audio session
	// interruption.
	interrupted atomic.Bool
)

const observerClassName = "HWCameraAudioSessionObserver"

// loadPlatform binds AVAudioSession and registers the observer of its
// notifications. The HAL is not touched: CoreAudio.framework has none of
// its symbols on iOS.
func loadPlatform(open opener) error {
	// AVFAudio holds AVAudioSession since iOS 14.5; AVFoundation re-exports
	// it and is enough on older systems.
	_, _ = purego.Dlopen("/System/Library/Frameworks/AVFAudio.framework/AVFAudio", rtldLazy|rtldGlobal)
	if _, err := open("/System/Library/Frameworks/AVFoundation.framework/AVFoundation"); err != nil {
		return err
	}
	_, _ = purego.Dlopen("/System/Library/Frameworks/UIKit.framework/UIKit", rtldLazy|rtldGlobal)

	var missing []string
	sym := func(dst *objc.ID, name string, optional bool) {
		addr, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
		if err != nil || addr == 0 {
			if !optional {
				missing = append(missing, name)
			}
			return
		}
		// The symbol is the address of the NSString * variable.
		*dst = *(*objc.ID)(unsafe.Add(unsafe.Pointer(nil), addr))
	}
	sym(&avCategoryPlayAndRecord, "AVAudioSessionCategoryPlayAndRecord", false)
	sym(&avCategoryRecord, "AVAudioSessionCategoryRecord", false)
	sym(&avModeDefault, "AVAudioSessionModeDefault", false)
	sym(&avInterruptionNotification, "AVAudioSessionInterruptionNotification", false)
	sym(&avInterruptionTypeKey, "AVAudioSessionInterruptionTypeKey", false)
	sym(&avMediaServicesResetNotification, "AVAudioSessionMediaServicesWereResetNotification", true)
	sym(&uiAppDidBecomeActiveNotification, "UIApplicationDidBecomeActiveNotification", true)
	if len(missing) > 0 {
		return errors.New("coreaudio: missing AVAudioSession symbols: " + strings.Join(missing, ", "))
	}
	if objc.GetClass("AVAudioSession") == 0 {
		return errors.New("coreaudio: AVAudioSession class not found")
	}

	r := objc.RegisterName
	selSharedInstance, selCategory = r("sharedInstance"), r("category")
	selSetCategoryModeOptions = r("setCategory:mode:options:error:")
	selSetActive = r("setActive:error:")
	selIsInputAvailable = r("isInputAvailable")
	selAvailableInputs, selPreferredInput = r("availableInputs"), r("preferredInput")
	selSetPreferredInput = r("setPreferredInput:error:")
	selCurrentRoute, selInputs = r("currentRoute"), r("inputs")
	selPortName, selUID, selChannels = r("portName"), r("UID"), r("channels")
	selSampleRate, selRecordPermission = r("sampleRate"), r("recordPermission")
	selRequestRecordPermission = r("requestRecordPermission:")
	selRequestRecordPermissionHandler = r("requestRecordPermissionWithCompletionHandler:")
	selRespondsToSelector, selIsEqualToString = r("respondsToSelector:"), r("isEqualToString:")
	selUTF8String, selCount, selObjectAtIndex = r("UTF8String"), r("count"), r("objectAtIndex:")
	selCode, selLocalizedDescription = r("code"), r("localizedDescription")
	selDefaultCenter, selAddObserver = r("defaultCenter"), r("addObserver:selector:name:object:")
	selUserInfo, selObjectForKey = r("userInfo"), r("objectForKey:")
	selUnsignedIntegerValue = r("unsignedIntegerValue")
	selAlloc, selInit = r("alloc"), r("init")
	selOnInterruption = r("hwcameraAudioSessionInterruption:")
	selOnMediaReset = r("hwcameraAudioSessionMediaServicesReset:")
	selOnBecameActive = r("hwcameraApplicationDidBecomeActive:")
	return registerObserver()
}

// registerObserver creates the one observer of the session's
// notifications; it lives for the rest of the process and acts on every
// open stream.
func registerObserver() error {
	cls, err := objc.RegisterClass(observerClassName, objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: selOnInterruption, Fn: onInterruption},
		{Cmd: selOnMediaReset, Fn: onMediaServicesReset},
		{Cmd: selOnBecameActive, Fn: onBecameActive},
	})
	if err != nil {
		return err
	}
	withPool(func() {
		obs := objc.ID(cls).Send(selAlloc).Send(selInit)
		center := class("NSNotificationCenter").Send(selDefaultCenter)
		// object nil: AVAudioSession posts from its shared instance, but
		// nil cannot miss a notification.
		center.Send(selAddObserver, obs, selOnInterruption, avInterruptionNotification, objc.ID(0))
		if avMediaServicesResetNotification != 0 {
			center.Send(selAddObserver, obs, selOnMediaReset, avMediaServicesResetNotification, objc.ID(0))
		}
		if uiAppDidBecomeActiveNotification != 0 {
			center.Send(selAddObserver, obs, selOnBecameActive, uiAppDidBecomeActiveNotification, objc.ID(0))
		}
	})
	return nil
}

func eachStream(fn func(*stream)) {
	streams.Range(func(_, v any) bool {
		if s := v.(*stream); !s.closing.Load() {
			fn(s)
		}
		return true
	})
}

// onInterruption observes AVAudioSessionInterruptionNotification. The
// system stops (or pauses) the queues when an interruption begins; they
// are started again when it ends. Apple's AVAudioSessionInterruptionOption
// ShouldResume is not required: a recorder resumes anyway, and when the
// start fails the stream waits for the app to become active.
func onInterruption(_ objc.ID, _ objc.SEL, note objc.ID) {
	typ := -1
	if note != 0 {
		if ui := note.Send(selUserInfo); ui != 0 {
			if n := ui.Send(selObjectForKey, avInterruptionTypeKey); n != 0 {
				typ = int(n.Send(selUnsignedIntegerValue))
			}
		}
	}
	switch typ {
	case avInterruptionTypeBegan:
		interrupted.Store(true)
		eachStream(func(s *stream) { s.paused.Store(true) })
	case avInterruptionTypeEnded:
		interrupted.Store(false)
		eachStream(func(s *stream) { go s.resume() })
	}
}

// onMediaServicesReset observes
// AVAudioSessionMediaServicesWereResetNotification: every audio object
// of the process, the queues included, is invalid afterwards.
func onMediaServicesReset(_ objc.ID, _ objc.SEL, _ objc.ID) {
	interrupted.Store(false)
	eachStream(func(s *stream) {
		s.ring.Fail(&audio.BackendError{Backend: Name, Op: "media services were reset", Err: audio.ErrNoDevice})
	})
}

// onBecameActive observes UIApplicationDidBecomeActiveNotification: an
// interruption does not always end with a notification.
func onBecameActive(_ objc.ID, _ objc.SEL, _ objc.ID) {
	interrupted.Store(false)
	eachStream(func(s *stream) { go s.resume() })
}

// queueStopped handles the queue stopping by itself, which on iOS is an
// interruption: Read keeps blocking. The interruption notification may
// arrive after the queue stopped, or (for a stop that is not an
// interruption) not at all, so a restart is tried shortly afterwards
// unless an interruption has begun by then.
func (s *stream) queueStopped() {
	s.paused.Store(true)
	time.AfterFunc(500*time.Millisecond, func() {
		if !interrupted.Load() {
			s.resume()
		}
	})
}

// resume reactivates the session and starts a paused queue again. On
// failure the stream stays paused until the next interruption end or app
// activation.
func (s *stream) resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing.Load() || !s.paused.Load() {
		return
	}
	withPool(func() { _ = activate(sharedSession()) })
	if st := audioQueueStart(s.queue, nil); st != 0 {
		return
	}
	s.paused.Store(false)
	s.resumed.Store(true)
}

func sharedSession() objc.ID { return class("AVAudioSession").Send(selSharedInstance) }

func responds(obj objc.ID, sel objc.SEL) bool {
	return obj != 0 && objc.Send[bool](obj, selRespondsToSelector, sel)
}

func equalStrings(a, b objc.ID) bool {
	return a != 0 && b != 0 && objc.Send[bool](a, selIsEqualToString, b)
}

func nsGoString(s objc.ID) string {
	if s == 0 {
		return ""
	}
	p := unsafe.Add(unsafe.Pointer(nil), uintptr(s.Send(selUTF8String)))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

func items(arr objc.ID) []objc.ID {
	if arr == 0 {
		return nil
	}
	n := int(arr.Send(selCount))
	out := make([]objc.ID, n)
	for i := range out {
		out[i] = arr.Send(selObjectAtIndex, uintptr(i))
	}
	return out
}

// sessionErr turns an AVAudioSession NSError into a BackendError.
func sessionErr(op string, nserr objc.ID) error {
	var code int64
	var err error
	if nserr != 0 {
		code = int64(nserr.Send(selCode))
		err = errors.New(nsGoString(nserr.Send(selLocalizedDescription)))
	}
	var sentinel error
	switch code {
	case avSessionErrCannotInterruptOthers, avSessionErrInsufficientPriority, avSessionErrSiriIsRecording,
		avSessionErrCannotStartRecording, avSessionErrIsBusy:
		sentinel = audio.ErrBusy
	case avSessionErrMissingEntitlement:
		sentinel = audio.ErrPermission
	}
	switch {
	case sentinel != nil && err != nil:
		err = errors.Join(sentinel, err)
	case sentinel != nil:
		err = sentinel
	case err == nil:
		err = errors.New("failed")
	}
	return &audio.BackendError{Backend: Name, Op: op, Code: code, Err: err}
}

// configureCategory makes the session able to record. A category the app
// chose that already records (PlayAndRecord, Record) is kept with its mode
// and options; anything else (the default SoloAmbient, Ambient, Playback)
// becomes PlayAndRecord, which keeps the app's audio output working,
// with Bluetooth headset input and the speaker (not the receiver) as the
// default output.
func configureCategory(sess objc.ID) error {
	cat := sess.Send(selCategory)
	if equalStrings(cat, avCategoryPlayAndRecord) || equalStrings(cat, avCategoryRecord) {
		return nil
	}
	var nserr objc.ID
	if !objc.Send[bool](sess, selSetCategoryModeOptions, avCategoryPlayAndRecord, avModeDefault,
		avCategoryOptionAllowBluetooth|avCategoryOptionDefaultToSpeaker, &nserr) {
		return sessionErr("AVAudioSession setCategory", nserr)
	}
	return nil
}

func activate(sess objc.ID) error {
	var nserr objc.ID
	if !objc.Send[bool](sess, selSetActive, true, &nserr) {
		return sessionErr("AVAudioSession setActive", nserr)
	}
	return nil
}

// defaultInputUID is the app's preferred input, else the input of the
// current route.
func defaultInputUID(sess objc.ID) string {
	if p := sess.Send(selPreferredInput); p != 0 {
		if uid := nsGoString(p.Send(selUID)); uid != "" {
			return uid
		}
	}
	if route := sess.Send(selCurrentRoute); route != 0 {
		for _, p := range items(route.Send(selInputs)) {
			if uid := nsGoString(p.Send(selUID)); uid != "" {
				return uid
			}
		}
	}
	return ""
}

// platformDevices lists AVAudioSession.availableInputs, the default
// first. availableInputs is only filled while the category can record, so
// the category is set first (see configureCategory).
func platformDevices() ([]audio.DeviceInfo, error) {
	var out []audio.DeviceInfo
	var err error
	withPool(func() {
		sess := sharedSession()
		if err = configureCategory(sess); err != nil {
			return
		}
		def := defaultInputUID(sess)
		// The hardware rate is the session's, shared by every port.
		rate := int(objc.Send[float64](sess, selSampleRate) + 0.5)
		for _, p := range items(sess.Send(selAvailableInputs)) {
			uid := nsGoString(p.Send(selUID))
			if uid == "" {
				continue
			}
			name := nsGoString(p.Send(selPortName))
			if name == "" {
				name = uid
			}
			d := audio.DeviceInfo{
				ID:         uid,
				Name:       name,
				Backend:    Name,
				Default:    uid == def,
				SampleRate: rate,
				// channels is nil for a port that is not routed; 0 means
				// unknown.
				Channels: len(items(p.Send(selChannels))),
			}
			if d.Default {
				out = append([]audio.DeviceInfo{d}, out...)
			} else {
				out = append(out, d)
			}
		}
		if def == "" && len(out) > 0 {
			out[0].Default = true
		}
	})
	return out, err
}

// prepareInput asks for permission, makes the session record-capable and
// active, and routes the chosen port. The queue follows the session's
// route, so no device is bound to it: kAudioQueueProperty_CurrentDevice
// is never set on iOS.
func prepareInput(ctx context.Context, id string) (string, error) {
	if err := authorize(ctx); err != nil {
		return "", err
	}
	var err error
	withPool(func() {
		sess := sharedSession()
		if err = configureCategory(sess); err != nil {
			return
		}
		if err = activate(sess); err != nil {
			return
		}
		if !objc.Send[bool](sess, selIsInputAvailable) {
			err = audio.ErrNoDevice
			return
		}
		if id == "" {
			return
		}
		var port objc.ID
		for _, p := range items(sess.Send(selAvailableInputs)) {
			if nsGoString(p.Send(selUID)) == id {
				port = p
				break
			}
		}
		if port == 0 {
			err = audio.ErrNotFound
			return
		}
		var nserr objc.ID
		if !objc.Send[bool](sess, selSetPreferredInput, port, &nserr) {
			err = sessionErr("AVAudioSession setPreferredInput", nserr)
		}
	})
	return "", err
}

// recordApp returns AVAudioApplication (iOS 17) when it exists.
func recordApp() objc.ID {
	c := class("AVAudioApplication")
	if c == 0 || !responds(c, selSharedInstance) {
		return 0
	}
	return c.Send(selSharedInstance)
}

// authorizationStatus maps the record permission to the AVAuthorizationStatus
// values the shared code uses.
func authorizationStatus() int {
	var p uintptr
	withPool(func() {
		if app := recordApp(); app != 0 && responds(app, selRecordPermission) {
			p = uintptr(app.Send(selRecordPermission))
			return
		}
		p = uintptr(sharedSession().Send(selRecordPermission))
	})
	switch p {
	case recordPermissionGranted:
		return authAuthorized
	case recordPermissionDenied:
		return authDenied
	case recordPermissionUndetermined:
		return authNotDetermined
	}
	return authUnknown
}

// requestAccess asks the user for microphone access; the channel receives
// once the user has answered.
func requestAccess() <-chan struct{} {
	ch := make(chan struct{}, 1)
	withPool(func() {
		// The BOOL argument is not trusted; the caller reads the status
		// again.
		blk := objc.NewBlock(func(_ objc.Block, _ bool) {
			select {
			case ch <- struct{}{}:
			default:
			}
		})
		if c := class("AVAudioApplication"); c != 0 && responds(c, selRequestRecordPermissionHandler) {
			c.Send(selRequestRecordPermissionHandler, blk)
		} else {
			sharedSession().Send(selRequestRecordPermission, blk)
		}
		// The callee copied the block; drop our reference.
		blk.Release()
	})
	return ch
}

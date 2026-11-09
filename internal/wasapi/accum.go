package wasapi

import (
	"time"

	"github.com/shibukawa/hwcamera/internal/audio"
)

// accumulator regroups WASAPI packets (whose size follows the engine
// period, typically 10 ms) into buffers of exactly cfg.PeriodFrames()
// frames, so Read returns consistently sized blocks.
//
// Times are in REFERENCE_TIME (100 ns) units. The timestamp of a buffer is
// the time of its first frame relative to the first packet of the stream.
type accumulator struct {
	cfg          audio.Config
	frameBytes   int
	periodFrames int

	buf   []byte // pending frames, len < periodFrames*frameBytes
	start int64  // time of buf[0]
	disc  bool   // a discontinuity occurred inside or before buf

	base    int64
	baseSet bool
}

func newAccumulator(cfg audio.Config) *accumulator {
	a := &accumulator{
		cfg:          cfg,
		frameBytes:   cfg.Channels * cfg.Format.Size(),
		periodFrames: cfg.PeriodFrames(),
	}
	a.buf = make([]byte, 0, a.periodFrames*a.frameBytes)
	return a
}

// add appends one packet of frames frames captured at time t (100 ns
// units). data is the packet payload, or nil for a silent packet (zeros).
// disc marks a gap before the packet. Completed buffers are passed to emit.
func (a *accumulator) add(data []byte, frames int, t int64, disc bool, emit func(*audio.Buffer)) {
	if frames <= 0 {
		return
	}
	if !a.baseSet {
		a.base, a.baseSet = t, true
		disc = false // nothing was lost before the first packet
	}
	if disc {
		a.disc = true
	}
	full := a.periodFrames * a.frameBytes
	off := 0 // frames consumed from this packet
	for off < frames {
		if len(a.buf) == 0 {
			a.start = t + int64(off)*10_000_000/int64(a.cfg.SampleRate)
		}
		have := len(a.buf) / a.frameBytes
		n := min(a.periodFrames-have, frames-off)
		if data == nil {
			a.buf = append(a.buf, make([]byte, n*a.frameBytes)...)
		} else {
			a.buf = append(a.buf, data[off*a.frameBytes:(off+n)*a.frameBytes]...)
		}
		off += n
		if len(a.buf) == full {
			emit(a.take())
		}
	}
}

// take returns the pending frames as a buffer and resets the accumulator.
func (a *accumulator) take() *audio.Buffer {
	data := a.buf
	a.buf = make([]byte, 0, a.periodFrames*a.frameBytes)
	b := &audio.Buffer{
		Data:          data,
		Format:        a.cfg.Format,
		SampleRate:    a.cfg.SampleRate,
		Channels:      a.cfg.Channels,
		Frames:        len(data) / a.frameBytes,
		Timestamp:     time.Duration(a.start-a.base) * 100,
		Discontinuity: a.disc,
	}
	a.disc = false
	return b
}

// timestamper chooses the capture time of each packet in 100 ns units.
// It uses the QPC position GetBuffer reports (already in 100 ns units) when
// the first packet carries one, otherwise the device position in frames.
// A packet flagged AUDCLNT_BUFFERFLAGS_TIMESTAMP_ERROR is placed right
// after the previous one.
type timestamper struct {
	rate    int64
	started bool
	useQPC  bool
	next    int64 // expected time of the next packet
}

func (ts *timestamper) time(frames int, flags uint32, devPos, qpc uint64) int64 {
	if !ts.started {
		ts.started = true
		ts.useQPC = qpc != 0 && flags&AUDCLNT_BUFFERFLAGS_TIMESTAMP_ERROR == 0
	}
	var t int64
	switch {
	case flags&AUDCLNT_BUFFERFLAGS_TIMESTAMP_ERROR != 0:
		t = ts.next
	case ts.useQPC:
		t = int64(qpc)
	default:
		t = int64(devPos) * 10_000_000 / ts.rate
	}
	ts.next = t + int64(frames)*10_000_000/ts.rate
	return t
}

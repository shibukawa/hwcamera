//go:build linux && (amd64 || arm64 || riscv64 || loong64 || ppc64le || ppc64 || s390x || mips64 || mips64le)

// Package v4l2 is the Linux backend: the Video4Linux2 capture API driven
// through ioctl and mmap on /dev/videoN, with no library in between.
//
// The structures below mirror include/uapi/linux/videodev2.h for 64-bit
// kernels (the layouts of struct v4l2_buffer and struct v4l2_format depend
// on the size of a pointer and of struct timeval). abi_test.go pins their
// sizes and the ioctl numbers derived from them.
package v4l2

import "unsafe"

const (
	iocWrite = 1
	iocRead  = 2
)

func ioc(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'V'<<8 | nr }
func ior(nr, size uintptr) uintptr      { return ioc(iocRead, nr, size) }
func iow(nr, size uintptr) uintptr      { return ioc(iocWrite, nr, size) }
func iowr(nr, size uintptr) uintptr     { return ioc(iocRead|iocWrite, nr, size) }

type capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

type fmtdesc struct {
	Index       uint32
	Type        uint32
	Flags       uint32
	Description [32]byte
	PixelFormat uint32
	MbusCode    uint32
	Reserved    [3]uint32
}

type frmsizeenum struct {
	Index       uint32
	PixelFormat uint32
	Type        uint32
	// union { discrete {width, height}; stepwise {min_width, max_width,
	// step_width, min_height, max_height, step_height} }
	U        [6]uint32
	Reserved [2]uint32
}

type frmivalenum struct {
	Index       uint32
	PixelFormat uint32
	Width       uint32
	Height      uint32
	Type        uint32
	// union { discrete fract; stepwise {min, max, step} fract }
	U        [6]uint32
	Reserved [2]uint32
}

type pixFormat struct {
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	YcbcrEnc     uint32
	Quantization uint32
	XferFunc     uint32
}

// format is struct v4l2_format: the union is 8-byte aligned because one of
// its members (struct v4l2_window) holds pointers.
type format struct {
	Type uint32
	_    uint32
	Fmt  [200]byte
}

func (f *format) pix() *pixFormat { return (*pixFormat)(unsafe.Pointer(&f.Fmt[0])) }

type captureParm struct {
	Capability   uint32
	CaptureMode  uint32
	TimePerFrame [2]uint32 // numerator, denominator
	ExtendedMode uint32
	ReadBuffers  uint32
	Reserved     [4]uint32
}

type streamparm struct {
	Type uint32
	Parm [200]byte
}

func (s *streamparm) capture() *captureParm { return (*captureParm)(unsafe.Pointer(&s.Parm[0])) }

type requestbuffers struct {
	Count        uint32
	Type         uint32
	Memory       uint32
	Capabilities uint32
	Flags        uint8
	Reserved     [3]uint8
}

type timeval struct {
	Sec  int64
	Usec int64
}

type timecode struct {
	Type     uint32
	Flags    uint32
	Frames   uint8
	Seconds  uint8
	Minutes  uint8
	Hours    uint8
	Userbits [4]uint8
}

type buffer struct {
	Index     uint32
	Type      uint32
	BytesUsed uint32
	Flags     uint32
	Field     uint32
	Timestamp timeval
	Timecode  timecode
	Sequence  uint32
	Memory    uint32
	M         uint64 // union { __u32 offset; unsigned long userptr; struct v4l2_plane *planes; __s32 fd; }
	Length    uint32
	Reserved2 uint32
	RequestFD int32
}

const (
	capVideoCapture      = 0x00000001
	capVideoCaptureMPane = 0x00001000
	capStreaming         = 0x04000000
	capDeviceCaps        = 0x80000000

	bufTypeVideoCapture = 1
	memoryMMAP          = 1
	fieldAny            = 0

	frmsizeDiscrete   = 1
	frmsizeContinuous = 2
	frmsizeStepwise   = 3

	frmivalDiscrete = 1

	capTimePerFrame = 0x1000

	bufFlagError = 0x40
)

var (
	vidiocQuerycap           = ior(0, unsafe.Sizeof(capability{}))
	vidiocEnumFmt            = iowr(2, unsafe.Sizeof(fmtdesc{}))
	vidiocGFmt               = iowr(4, unsafe.Sizeof(format{}))
	vidiocSFmt               = iowr(5, unsafe.Sizeof(format{}))
	vidiocReqbufs            = iowr(8, unsafe.Sizeof(requestbuffers{}))
	vidiocQuerybuf           = iowr(9, unsafe.Sizeof(buffer{}))
	vidiocQbuf               = iowr(15, unsafe.Sizeof(buffer{}))
	vidiocDqbuf              = iowr(17, unsafe.Sizeof(buffer{}))
	vidiocStreamon           = iow(18, unsafe.Sizeof(int32(0)))
	vidiocStreamoff          = iow(19, unsafe.Sizeof(int32(0)))
	vidiocGParm              = iowr(21, unsafe.Sizeof(streamparm{}))
	vidiocSParm              = iowr(22, unsafe.Sizeof(streamparm{}))
	vidiocEnumFramesizes     = iowr(74, unsafe.Sizeof(frmsizeenum{}))
	vidiocEnumFrameintervals = iowr(75, unsafe.Sizeof(frmivalenum{}))
)

func fourcc(s string) uint32 {
	return uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
}

func fourccString(v uint32) string {
	b := []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	return string(b)
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

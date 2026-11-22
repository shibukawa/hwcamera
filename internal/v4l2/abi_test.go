//go:build linux && !android && (amd64 || arm64 || riscv64 || loong64 || ppc64le || ppc64 || s390x || mips64 || mips64le)

package v4l2

import (
	"testing"
	"unsafe"
)

// The ioctl numbers of videodev2.h on 64-bit Linux, as printed by a C
// program built against the kernel headers.
func TestIoctlNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"VIDIOC_QUERYCAP", vidiocQuerycap, 0x80685600},
		{"VIDIOC_ENUM_FMT", vidiocEnumFmt, 0xc0405602},
		{"VIDIOC_G_FMT", vidiocGFmt, 0xc0d05604},
		{"VIDIOC_S_FMT", vidiocSFmt, 0xc0d05605},
		{"VIDIOC_REQBUFS", vidiocReqbufs, 0xc0145608},
		{"VIDIOC_QUERYBUF", vidiocQuerybuf, 0xc0585609},
		{"VIDIOC_QBUF", vidiocQbuf, 0xc058560f},
		{"VIDIOC_DQBUF", vidiocDqbuf, 0xc0585611},
		{"VIDIOC_STREAMON", vidiocStreamon, 0x40045612},
		{"VIDIOC_STREAMOFF", vidiocStreamoff, 0x40045613},
		{"VIDIOC_G_PARM", vidiocGParm, 0xc0cc5615},
		{"VIDIOC_S_PARM", vidiocSParm, 0xc0cc5616},
		{"VIDIOC_ENUM_FRAMESIZES", vidiocEnumFramesizes, 0xc02c564a},
		{"VIDIOC_ENUM_FRAMEINTERVALS", vidiocEnumFrameintervals, 0xc034564b},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

func TestBufferLayout(t *testing.T) {
	var b buffer
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"timestamp", unsafe.Offsetof(b.Timestamp), 24},
		{"timecode", unsafe.Offsetof(b.Timecode), 40},
		{"sequence", unsafe.Offsetof(b.Sequence), 56},
		{"memory", unsafe.Offsetof(b.Memory), 60},
		{"m", unsafe.Offsetof(b.M), 64},
		{"length", unsafe.Offsetof(b.Length), 72},
		{"request_fd", unsafe.Offsetof(b.RequestFD), 80},
	} {
		if c.got != c.want {
			t.Errorf("v4l2_buffer.%s at %d, want %d", c.name, c.got, c.want)
		}
	}
	var f format
	if off := unsafe.Offsetof(f.Fmt); off != 8 {
		t.Errorf("v4l2_format.fmt at %d, want 8", off)
	}
	if n := unsafe.Sizeof(pixFormat{}); n != 48 {
		t.Errorf("v4l2_pix_format is %d bytes, want 48", n)
	}
}

func TestFourcc(t *testing.T) {
	if fourcc("YUYV") != 0x56595559 {
		t.Fatalf("fourcc(YUYV) = %#x", fourcc("YUYV"))
	}
	if fourccString(fourcc("MJPG")) != "MJPG" {
		t.Fatal("round trip")
	}
	if rateToInterval(29.97) != [2]uint32{1000, 29970} || rateToInterval(30) != [2]uint32{1, 30} {
		t.Fatal("rateToInterval")
	}
}

//go:build linux && !android && (amd64 || arm64 || riscv64 || loong64 || ppc64le || ppc64 || s390x || mips64 || mips64le)

package v4l2

import (
	"testing"

	"github.com/shibukawa/hwcamera/internal/camera"
)

func TestPosition(t *testing.T) {
	for _, c := range []struct {
		bus, driver, removable string
		want                   camera.Position
	}{
		{"usb-0000:00:14.0-5", "uvcvideo", "fixed", camera.PositionFront},
		{"usb-0000:00:14.0-2", "uvcvideo", "removable", camera.PositionExternal},
		{"usb-0000:00:14.0-2", "uvcvideo", "unknown", camera.PositionUnknown},
		{"platform:vivid-000", "vivid", "", camera.PositionExternal},
		{"platform:rkisp1", "rkisp1", "", camera.PositionFront},
		{"PCI:0000:01:00.0", "cx23885", "", camera.PositionUnknown},
	} {
		if got := position(c.bus, c.driver, c.removable); got != c.want {
			t.Errorf("%s/%s/%s: %s, want %s", c.bus, c.driver, c.removable, got, c.want)
		}
	}
}

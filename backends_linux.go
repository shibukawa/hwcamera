//go:build linux && (amd64 || arm64 || riscv64 || loong64 || ppc64le || ppc64 || s390x || mips64 || mips64le)

package hwcamera

import (
	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/v4l2"
)

func init() {
	camera.Register(v4l2.Backend{})
}

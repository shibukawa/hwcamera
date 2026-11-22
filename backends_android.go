//go:build android

package hwcamera

import (
	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/camera2"
)

func init() { camera.Register(camera2.Backend{}) }

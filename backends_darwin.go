//go:build darwin

package hwcamera

import (
	"github.com/shibukawa/hwcamera/internal/avfoundation"
	"github.com/shibukawa/hwcamera/internal/camera"
)

func init() { camera.Register(avfoundation.Backend{}) }

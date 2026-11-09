//go:build windows

package hwcamera

import (
	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/mediafoundation"
)

func init() { camera.Register(mediafoundation.Backend{}) }

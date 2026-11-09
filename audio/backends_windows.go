//go:build windows

package audio

import (
	iaudio "github.com/shibukawa/hwcamera/internal/audio"
	"github.com/shibukawa/hwcamera/internal/wasapi"
)

func init() { iaudio.Register(wasapi.Backend{}) }

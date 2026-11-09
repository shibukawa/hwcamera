//go:build darwin

package audio

import (
	iaudio "github.com/shibukawa/hwcamera/internal/audio"
	"github.com/shibukawa/hwcamera/internal/coreaudio"
)

func init() { iaudio.Register(coreaudio.Backend{}) }

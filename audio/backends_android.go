//go:build android

package audio

import (
	"github.com/shibukawa/hwcamera/internal/aaudio"
	iaudio "github.com/shibukawa/hwcamera/internal/audio"
)

func init() { iaudio.Register(aaudio.Backend{}) }

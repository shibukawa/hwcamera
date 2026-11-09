//go:build linux && !android && (amd64 || arm64 || riscv64 || loong64 || ppc64le || s390x)

package audio

import (
	iaudio "github.com/shibukawa/hwcamera/internal/audio"
	"github.com/shibukawa/hwcamera/internal/pulse"
)

func init() { iaudio.Register(pulse.Backend{}) }

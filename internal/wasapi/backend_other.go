//go:build !windows || !(amd64 || arm64)

package wasapi

import (
	"context"
	"fmt"

	"github.com/shibukawa/hwcamera/internal/audio"
)

// Available reports false: WASAPI is only bound on windows/amd64 and
// windows/arm64.
func (Backend) Available() bool { return false }

// Devices returns audio.ErrUnsupported.
func (Backend) Devices(context.Context) ([]audio.DeviceInfo, error) {
	return nil, fmt.Errorf("%w: WASAPI backend not built for this platform", audio.ErrUnsupported)
}

// Open returns audio.ErrUnsupported.
func (Backend) Open(context.Context, string, audio.Config) (audio.Stream, error) {
	return nil, fmt.Errorf("%w: WASAPI backend not built for this platform", audio.ErrUnsupported)
}

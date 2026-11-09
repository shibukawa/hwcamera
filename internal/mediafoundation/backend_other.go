//go:build !windows || !(amd64 || arm64)

package mediafoundation

import (
	"context"
	"fmt"

	"github.com/shibukawa/hwcamera/internal/camera"
)

// Available reports false: Media Foundation is only bound on windows/amd64
// and windows/arm64.
func (Backend) Available() bool { return false }

// Devices returns camera.ErrUnsupported.
func (Backend) Devices(context.Context) ([]camera.DeviceInfo, error) {
	return nil, fmt.Errorf("%w: Media Foundation backend not built for this platform", camera.ErrUnsupported)
}

// Open returns camera.ErrUnsupported.
func (Backend) Open(context.Context, string, camera.Config) (camera.Stream, error) {
	return nil, fmt.Errorf("%w: Media Foundation backend not built for this platform", camera.ErrUnsupported)
}

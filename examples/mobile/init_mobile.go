//go:build android || ios

package mobile

import (
	"github.com/hajimehoshi/ebiten/v2/mobile"

	"github.com/shibukawa/hwcamera/examples/internal/camgame"
)

// On a phone or tablet the game starts as soon as the bound library is
// loaded; the native code only has to show its view.
func init() {
	mobile.SetGame(camgame.New())
}

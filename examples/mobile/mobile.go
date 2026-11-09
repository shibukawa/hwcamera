// Package mobile is the hwcamera sample as an Ebitengine mobile game:
// `ebitenmobile bind` turns it into an iOS xcframework that the Xcode
// project in ios/ embeds, and into an Android aar for the project in
// android/ (see README.md in this directory). The game itself is
// examples/internal/camgame; mobile/desktop runs the same game in a window.
package mobile

// Dummy exists for gomobile, which binds a package only when it exports
// something.
func Dummy() {}

module github.com/shibukawa/hwcamera/examples

go 1.27.0

require (
	github.com/hajimehoshi/ebiten/v2 v2.10.4
	github.com/shibukawa/hwcamera v0.0.0-00010101000000-000000000000
	github.com/shibukawa/hwcamera/mediacodec v0.0.0-00010101000000-000000000000
	github.com/shibukawa/hwmediacodec v0.5.1
	github.com/shibukawa/hwnpu v0.0.0-20261109005857-aea7411a2fe0
)

require (
	github.com/Eyevinn/mp4ff v0.59.0 // indirect
	github.com/ebitengine/gomobile v0.0.0-20260820040257-d11f821a26a6 // indirect
	github.com/ebitengine/hideconsole v1.0.0 // indirect
	github.com/ebitengine/purego v0.11.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

replace (
	github.com/shibukawa/hwcamera => ../
	github.com/shibukawa/hwcamera/mediacodec => ../mediacodec
)

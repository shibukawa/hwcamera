module github.com/shibukawa/hwcamera/cmd

go 1.27.0

replace (
	github.com/shibukawa/hwcamera => ../
	github.com/shibukawa/hwcamera/mediacodec => ../mediacodec
)

require (
	github.com/ebitengine/debugui v0.3.1
	github.com/hajimehoshi/ebiten/v2 v2.10.6
	github.com/shibukawa/hwcamera v0.0.0-00010101000000-000000000000
	github.com/shibukawa/hwcamera/mediacodec v0.0.0-00010101000000-000000000000
	github.com/shibukawa/hwmediacodec v0.5.2
)

require (
	github.com/Eyevinn/mp4ff v0.59.0 // indirect
	github.com/ebitengine/gomobile v0.0.0-20260820040257-d11f821a26a6 // indirect
	github.com/ebitengine/hideconsole v1.0.0 // indirect
	github.com/ebitengine/purego v0.11.3 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/hajimehoshi/bitmapfont/v4 v4.2.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.29 // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

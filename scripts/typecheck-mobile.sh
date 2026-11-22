#!/bin/sh
# Type-checks the iOS and Android code paths on any machine, without Xcode
# or the NDK. Real mobile builds need cgo (purego uses the cgo runtime
# there); this only proves the Go code compiles.
#
#   iOS:     GOOS=darwin + the ios tag. purego refuses to compile for iOS
#            without cgo (is_ios.go), so a scratch copy without that file
#            stands in for it through a temporary go.mod.
#   Android: the NDK files carry `android || hwcamera_typecheck`, so they
#            compile for linux/arm64 with that tag and no cgo.
set -eu
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'chmod -R u+w "$tmp"; rm -rf "$tmp"' EXIT

purego=$(GOWORK=off go list -m -f '{{.Dir}}' github.com/ebitengine/purego)
cp -R "$purego" "$tmp/purego"
chmod -R u+w "$tmp/purego"
rm "$tmp/purego/is_ios.go"
cp go.mod "$tmp/go.mod"
cp go.sum "$tmp/go.sum"
printf '\nreplace github.com/ebitengine/purego => %s\n' "$tmp/purego" >> "$tmp/go.mod"

echo "== ios (darwin/arm64, -tags ios)"
GOWORK=off GOFLAGS="-modfile=$tmp/go.mod" GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
	go vet -tags ios ./internal/avfoundation ./internal/coreaudio . ./audio ./tensor ./cmd/...

echo "== android (linux/arm64, -tags hwcamera_typecheck)"
GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go vet -tags hwcamera_typecheck ./internal/camera2 ./internal/aaudio
GOWORK=off GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go vet -tags hwcamera_typecheck ./internal/camera2 ./internal/aaudio
echo ok

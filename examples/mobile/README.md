# mobile: the camera and microphone on an iPhone, iPad or Android device

This directory is the hwcamera sample as an Ebitengine *mobile* game: a Go
package without a `main`, which `ebitenmobile bind` compiles into a native
library that a normal Xcode or Android Studio project embeds. The game
(`../internal/camgame`) shows the camera full screen and a level meter of
the microphone, with buttons along the bottom:

| Button | Does |
| --- | --- |
| Camera | next camera (back, front, ultra wide, external, ...) |
| Mirror | mirror the picture (on by default for front cameras) |
| Rotate | turn the picture a quarter turn (camera sensors are mounted landscape) |
| Mic off / Mic on | stop or start the microphone |

The text at the top names the camera, its native mode and the frame rate
that arrives, and the microphone with its sample rate and the gaps (lost
buffers) so far. Errors show there too; a missing permission is retried
every second.

`ios/` is the Xcode project, `android/` the Android Studio project; both
embed the same Go code. `desktop/` runs the same game in a window:

```sh
cd examples
go run ./mobile/desktop
```

## Build and run on an iPhone or iPad

Requirements: Xcode with the iOS platform, Go 1.27 or later, and a device
(the Simulator has no camera). From the `examples` module:

```sh
cd examples
go run github.com/hajimehoshi/ebiten/v2/cmd/ebitenmobile bind -target ios -iosversion 15.0 \
    -o mobile/ios/Mobile.xcframework github.com/shibukawa/hwcamera/examples/mobile
open mobile/ios/HWCameraSample.xcodeproj
```

`ebitenmobile bind` builds the Go code with cgo enabled (purego needs the
cgo runtime on iOS; hwcamera itself has no cgo code) and writes
`Mobile.xcframework`, which the project references but which is not
checked in. In Xcode pick your team under Signing & Capabilities, choose
the device, and run. iOS asks for camera and microphone access on the
first start; the strings come from `ios/HWCameraSample/Info.plist`
(`NSCameraUsageDescription`, `NSMicrophoneUsageDescription`: without them
iOS ends the app at the first access).

## Build and run on an Android device

Requirements: the Android SDK with platform 35, build-tools and an NDK, a
JDK (17 or 21), Go 1.27 or later, and a device running Android 8.0 (API
26) or later with USB debugging on. On a Mac without Android Studio,
Homebrew's command-line tools are enough:

```sh
brew install --cask android-commandlinetools temurin@21
export ANDROID_HOME=/opt/homebrew/share/android-commandlinetools
export JAVA_HOME=$(/usr/libexec/java_home -v 21)
export PATH="$ANDROID_HOME/platform-tools:$PATH"
android sdk install platform-tools "platforms;android-35" "build-tools;35.0.0" "ndk;27.3.13750724"
```

Then, from the `examples` module:

```sh
cd examples
go run github.com/hajimehoshi/ebiten/v2/cmd/ebitenmobile bind -target android/arm64,android/amd64 \
    -androidapi 26 -javapkg jp.shibu.hwcamera \
    -o mobile/android/app/libs/mobile.aar github.com/shibukawa/hwcamera/examples/mobile
```

and either open `mobile/android` in Android Studio (it adds the Gradle
wrapper on first open) and run the `app` configuration, or build and
install the APK with the SDK's own tools, without Gradle:

```sh
mobile/android/build-apk.sh
```

The app asks for the camera and the microphone on the first start
(`MainActivity.java`): Go cannot ask for runtime permissions, so the Java
side does and the game keeps retrying until they are granted.

## What to expect

- The back camera opens first (the system's default), at 1280x720 or the
  nearest mode, delivered as NV12 by the OS and converted to RGBA in Go
  for the screen.
- Camera frames arrive in the sensor's orientation: on a phone held
  upright the picture is on its side until **Rotate** is pressed.
- The meter moves with the voice; the line shows the input's sample rate
  (the OS converts to the 48 kHz mono the game asks for).
- Sending the app to the background and back: iOS interrupts the capture
  session and resumes it; Android takes the camera away from a background
  app.

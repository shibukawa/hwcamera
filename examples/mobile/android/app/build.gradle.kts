plugins {
    id("com.android.application")
}

android {
    namespace = "jp.shibu.hwcamera.sample"
    compileSdk = 35

    defaultConfig {
        applicationId = "jp.shibu.hwcamera.sample"
        // Android 8.0: AAudio (API 26); the Camera2 NDK and AImageReader
        // are there from API 24.
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "1.0"
        ndk {
            // What `ebitenmobile bind -target android/arm64,android/amd64` builds.
            abiFilters += listOf("arm64-v8a", "x86_64")
        }
    }
    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    // Built by `ebitenmobile bind` (see ../../README.md); not checked in.
    implementation(files("libs/mobile.aar"))
}

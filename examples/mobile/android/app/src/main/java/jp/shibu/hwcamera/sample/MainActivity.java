package jp.shibu.hwcamera.sample;

import android.Manifest;
import android.app.Activity;
import android.content.pm.PackageManager;
import android.os.Bundle;
import android.view.WindowManager;

import jp.shibu.hwcamera.mobile.EbitenView;

// The sample as an Android application: one EbitenView, which runs the Go
// game bound into mobile.aar. Go cannot ask for runtime permissions, so
// the activity asks for the camera and the microphone here; the game
// retries opening them every second until they are granted. A plain
// Activity without AndroidX keeps the app buildable with the SDK tools
// alone (build-apk.sh) as well as with Gradle.
public class MainActivity extends Activity {
    private static final int PERMISSIONS = 1;
    private EbitenView view;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);
        view = findViewById(R.id.ebitenview);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);

        String[] wanted = {Manifest.permission.CAMERA, Manifest.permission.RECORD_AUDIO};
        boolean missing = false;
        for (String p : wanted) {
            missing |= checkSelfPermission(p) != PackageManager.PERMISSION_GRANTED;
        }
        if (missing) {
            requestPermissions(wanted, PERMISSIONS);
        }
    }

    // Ebitengine asks that the game be suspended while the activity is
    // paused. The camera keeps running in Go; Android takes it away from a
    // background app and the stream reports the error on screen.
    @Override
    protected void onPause() {
        super.onPause();
        view.suspendGame();
    }

    @Override
    protected void onResume() {
        super.onResume();
        view.resumeGame();
    }
}

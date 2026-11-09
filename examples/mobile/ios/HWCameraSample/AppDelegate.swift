import UIKit
import Mobile

// The whole application is one view controller from the xcframework that
// `ebitenmobile bind` produced from the Go package in the parent directory;
// the game it runs is set there (mobile.SetGame in init_mobile.go). The Go
// library asks for camera and microphone access itself through
// AVFoundation and AVAudioSession; Info.plist carries the usage strings
// iOS shows in those prompts.
@main
final class AppDelegate: UIResponder, UIApplicationDelegate {
    var window: UIWindow?
    private let game = MobileEbitenViewController()

    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        let window = UIWindow(frame: UIScreen.main.bounds)
        window.backgroundColor = .black
        window.rootViewController = game
        window.makeKeyAndVisible()
        self.window = window
        application.isIdleTimerDisabled = true
        return true
    }

    // Suspending the game loop in the background is what Ebitengine
    // recommends; iOS interrupts the capture session meanwhile and the
    // library resumes it when the app comes back.
    func applicationWillResignActive(_ application: UIApplication) {
        game.suspendGame()
    }

    func applicationDidBecomeActive(_ application: UIApplication) {
        game.resumeGame()
    }
}

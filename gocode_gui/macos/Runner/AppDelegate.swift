import Cocoa
import FlutterMacOS

@main
class AppDelegate: FlutterAppDelegate {
  override func applicationDidFinishLaunching(_ notification: Notification) {
    super.applicationDidFinishLaunching(notification)
    // The window is hidden (not closed) while the tray icon keeps the app
    // running — without this, macOS's automatic-termination heuristics can
    // decide a windowless app is idle and kill it out from under the tray
    // icon and any locally supervised gocode server.
    ProcessInfo.processInfo.disableAutomaticTermination(
      "Runs in the background via the menu bar icon")
    ProcessInfo.processInfo.disableSuddenTermination()
  }

  // The tray icon keeps the app running with the window hidden (not closed),
  // so losing the last visible window must not quit the app — returning true
  // here (the default Flutter scaffold value) made the tray's own hide toggle
  // kill the whole app on its first left click.
  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    return false
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }
}

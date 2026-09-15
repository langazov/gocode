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

  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    return true
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }
}

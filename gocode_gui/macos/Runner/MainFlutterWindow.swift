import Cocoa
import FlutterMacOS

// Ported from goide (app/macos/Runner/MainFlutterWindow.swift): the app
// draws its own title bar (lib/app/title_bar.dart) and this window makes
// room for it.
class MainFlutterWindow: NSWindow {
  /// Height of the app's title bar (`titleBarHeight` in
  /// lib/app/title_bar.dart); the traffic lights are centered in it.
  static let titleBarHeight: CGFloat = 40
  /// Left inset of the close button.
  static let trafficLightInset: CGFloat = 14

  private var channel: FlutterMethodChannel?
  /// The press that began the current drag: AppKit starts a window move
  /// from a mouse-down, and by the time Flutter recognizes a drag (and the
  /// channel call arrives) the current event is a drag or even the release.
  private var lastMouseDown: NSEvent?
  private var mouseDownMonitor: Any?

  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)
    self.minSize = NSSize(width: 480, height: 360)

    // Content extends under a transparent system title bar that keeps
    // only the traffic lights; the Flutter bar sits behind them.
    self.styleMask.insert(.fullSizeContentView)
    self.titlebarAppearsTransparent = true
    self.titleVisibility = .hidden
    self.isMovableByWindowBackground = false

    RegisterGeneratedPlugins(registry: flutterViewController)
    setUpChannel(flutterViewController)

    super.awakeFromNib()

    let center = NotificationCenter.default
    for name in [
      NSWindow.didResizeNotification, NSWindow.didEndLiveResizeNotification,
      NSWindow.didExitFullScreenNotification, NSWindow.didBecomeKeyNotification,
    ] {
      center.addObserver(forName: name, object: self, queue: .main) { [weak self] _ in
        self?.layoutTrafficLights()
      }
    }
    center.addObserver(forName: NSWindow.willEnterFullScreenNotification, object: self, queue: .main) {
      [weak self] _ in self?.channel?.invokeMethod("fullScreen", arguments: true)
    }
    center.addObserver(forName: NSWindow.willExitFullScreenNotification, object: self, queue: .main) {
      [weak self] _ in self?.channel?.invokeMethod("fullScreen", arguments: false)
    }
    layoutTrafficLights()
  }

  /// gocode/window: lets the Flutter title bar act like a native one.
  private func setUpChannel(_ controller: FlutterViewController) {
    let channel = FlutterMethodChannel(
      name: "gocode/window", binaryMessenger: controller.engine.binaryMessenger)
    channel.setMethodCallHandler { [weak self] call, result in
      guard let self = self else { return result(nil) }
      switch call.method {
      case "startDrag":
        // Only while the button is still down (a quick flick may already
        // be over), starting from the press itself.
        if NSEvent.pressedMouseButtons & 1 != 0, let down = self.lastMouseDown {
          self.performDrag(with: down)
        }
        self.lastMouseDown = nil
        result(nil)
      case "doubleClick":
        // Honor System Settings › Desktop & Dock › "Double-click a window's title bar to".
        switch UserDefaults.standard.string(forKey: "AppleActionOnDoubleClick") {
        case "Minimize": self.performMiniaturize(nil)
        case "None": break
        default: self.performZoom(nil)
        }
        result(nil)
      case "isFullScreen":
        result(self.styleMask.contains(.fullScreen))
      case "trafficLightsWidth":
        result(Double(self.trafficLightsWidth()))
      case "setAppearance":
        // The theme's light/dark kind and ground color: the traffic
        // lights and native menus follow it, and resizing never flashes a
        // different background.
        let args = call.arguments as? [String: Any] ?? [:]
        let dark = args["dark"] as? Bool ?? true
        self.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        if let argb = args["background"] as? Int {
          self.backgroundColor = NSColor(
            srgbRed: CGFloat((argb >> 16) & 0xFF) / 255,
            green: CGFloat((argb >> 8) & 0xFF) / 255,
            blue: CGFloat(argb & 0xFF) / 255,
            alpha: 1)
        }
        result(nil)
      default:
        result(FlutterMethodNotImplemented)
      }
    }
    self.channel = channel
    mouseDownMonitor = NSEvent.addLocalMonitorForEvents(matching: .leftMouseDown) {
      [weak self] event in
      if event.window === self { self?.lastMouseDown = event }
      return event
    }
  }

  /// Width the traffic lights occupy from the window's left edge.
  private func trafficLightsWidth() -> CGFloat {
    guard let zoom = standardWindowButton(.zoomButton) else { return 0 }
    return zoom.frame.maxX
  }

  /// Centers the traffic lights vertically in the app's title bar.
  private func layoutTrafficLights() {
    guard !styleMask.contains(.fullScreen),
      let close = standardWindowButton(.closeButton),
      let mini = standardWindowButton(.miniaturizeButton),
      let zoom = standardWindowButton(.zoomButton),
      let container = close.superview?.superview
    else { return }
    let height = MainFlutterWindow.titleBarHeight
    var frame = container.frame
    frame.size.height = height
    frame.origin.y = self.frame.height - height
    container.frame = frame
    let spacing = mini.frame.minX - close.frame.minX
    for (i, button) in [close, mini, zoom].enumerated() {
      button.setFrameOrigin(
        NSPoint(
          x: MainFlutterWindow.trafficLightInset + CGFloat(i) * spacing,
          // Centered above the bar's 1px bottom border.
          y: (height + 1 - button.frame.height) / 2))
    }
  }
}

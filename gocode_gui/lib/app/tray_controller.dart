import 'dart:async';
import 'dart:developer' as developer;
import 'dart:io';

import 'package:tray_manager/tray_manager.dart';
import 'package:window_manager/window_manager.dart';

/// Keeps the app alive with a menu bar icon after the window closes, instead
/// of quitting — so a locally supervised gocode server isn't torn down every
/// time the window is dismissed.
///
/// The window's own close button hides it rather than exiting (native close
/// is intercepted via [WindowManager.setPreventClose]); the tray icon's left
/// click toggles that same show/hide, and right click opens a small menu
/// with the real Quit.
class TrayController with TrayListener, WindowListener {
  TrayController._();

  static final instance = TrayController._();

  /// tray_manager/window_manager have no mobile or web implementation.
  static bool get isSupported =>
      Platform.isMacOS || Platform.isWindows || Platform.isLinux;

  bool _started = false;

  Future<void> start() async {
    if (!isSupported || _started) return;
    _started = true;

    await windowManager.ensureInitialized();
    await windowManager.setPreventClose(true);
    windowManager.addListener(this);

    await trayManager.setIcon('assets/icon/tray/tray_icon.png');
    await trayManager.setToolTip('Gocode Desktop');
    await trayManager.setContextMenu(
      Menu(
        items: [
          MenuItem(
            key: 'show',
            label: 'Show Gocode',
            onClick: (_) => _guard('show from menu', _show),
          ),
          MenuItem.separator(),
          MenuItem(
            key: 'quit',
            label: 'Quit',
            onClick: (_) => _guard('quit from menu', _quit),
          ),
        ],
      ),
    );
    trayManager.addListener(this);
  }

  Future<void> _show() async {
    await windowManager.show();
    await windowManager.focus();
  }

  Future<void> _toggle() async {
    if (await windowManager.isVisible()) {
      await windowManager.hide();
    } else {
      await _show();
    }
  }

  /// Bypasses the close-prevention this controller sets up, unlike the
  /// window's own close button.
  Future<void> _quit() => windowManager.destroy();

  /// Tray/window callbacks run outside any zone that would otherwise report
  /// their errors — an uncaught one here would vanish silently instead of
  /// just failing the one interaction it broke.
  void _guard(String action, Future<void> Function() body) {
    unawaited(
      body().catchError((Object e, StackTrace st) {
        developer.log('tray: $action failed', error: e, stackTrace: st);
      }),
    );
  }

  // WindowListener

  @override
  void onWindowClose() => _guard('hide on close', windowManager.hide);

  // TrayListener

  @override
  void onTrayIconMouseDown() => _guard('toggle', _toggle);

  @override
  void onTrayIconRightMouseDown() =>
      _guard('popup menu', trayManager.popUpContextMenu);
}

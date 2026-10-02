/// The app draws the window's title bar itself (macOS: the content extends
/// under a transparent system title bar; see
/// macos/Runner/MainFlutterWindow.swift). This bridges what a native title
/// bar does: room for the traffic lights, drag to move, double-click to
/// zoom. Ported from goide (app/lib/ui/window_chrome.dart).
library;

import 'dart:io' show Platform;

import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

const _channel = MethodChannel('gocode/window');

/// Whether the title bar shares the row with the macOS traffic lights.
final bool hasNativeTrafficLights = !kIsWeb && Platform.isMacOS;

/// Whether the app shows its own title bar: desktop only. On Windows and
/// Linux the native title bar stays and this one is a toolbar under it.
final bool showsAppTitleBar =
    !kIsWeb && (Platform.isMacOS || Platform.isWindows || Platform.isLinux);

/// Leading room for the traffic lights plus a gap. Their size differs
/// between macOS versions, so the window reports where they end.
class TrafficLightsInsetNotifier extends Notifier<double> {
  static const _gap = 12.0;

  @override
  double build() {
    if (hasNativeTrafficLights) {
      _channel.invokeMethod<double>('trafficLightsWidth').then((w) {
        if (ref.mounted && w != null && w > 0) state = w + _gap;
      }, onError: (_) {});
    }
    return 78;
  }
}

final trafficLightsInsetProvider =
    NotifierProvider<TrafficLightsInsetNotifier, double>(
      TrafficLightsInsetNotifier.new,
    );

class WindowFullScreenNotifier extends Notifier<bool> {
  @override
  bool build() {
    if (hasNativeTrafficLights) {
      _channel.setMethodCallHandler((call) async {
        if (call.method == 'fullScreen' && ref.mounted) {
          state = call.arguments == true;
        }
      });
      _channel.invokeMethod<bool>('isFullScreen').then((v) {
        if (ref.mounted) state = v ?? false;
      }, onError: (_) {});
    }
    return false;
  }
}

/// macOS full screen hides the traffic lights.
final windowFullScreenProvider =
    NotifierProvider<WindowFullScreenNotifier, bool>(
      WindowFullScreenNotifier.new,
    );

/// A title-bar background that behaves like a native one: drag moves the
/// window, double-click zooms (or what the user configured). Put it behind
/// the bar's controls (a Stack) so it only gets clicks on empty space; as a
/// parent the double-tap recognizer would delay every button tap.
class WindowDragArea extends StatelessWidget {
  const WindowDragArea({super.key, this.child = const SizedBox.expand()});

  final Widget child;

  @override
  Widget build(BuildContext context) {
    if (!hasNativeTrafficLights) return child;
    return RawGestureDetector(
      behavior: HitTestBehavior.opaque,
      gestures: {
        PanGestureRecognizer:
            GestureRecognizerFactoryWithHandlers<PanGestureRecognizer>(
              PanGestureRecognizer.new,
              (r) => r.onStart = (_) => _call('startDrag'),
            ),
        DoubleTapGestureRecognizer:
            GestureRecognizerFactoryWithHandlers<DoubleTapGestureRecognizer>(
              DoubleTapGestureRecognizer.new,
              (r) => r.onDoubleTap = () => _call('doubleClick'),
            ),
      },
      child: child,
    );
  }

  static void _call(String method) =>
      _channel.invokeMethod<void>(method).catchError((_) {});
}

/// Matches the native window (appearance, background) to the theme.
Future<void> setWindowAppearance({
  required bool dark,
  required Color background,
}) async {
  if (!hasNativeTrafficLights) return;
  try {
    await _channel.invokeMethod<void>('setAppearance', {
      'dark': dark,
      'background': background.toARGB32(),
    });
  } catch (_) {}
}

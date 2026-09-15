import 'dart:async';
import 'dart:ui';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'app/app.dart';
import 'app/tray_controller.dart';
import 'core/connection/controller.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();

  final container = ProviderContainer();
  await container.read(settingsProvider.notifier).load();

  // Kill the supervised server on quit. Without this the gocode child
  // outlives the app (macOS/Linux/Windows send no Dart cleanup on SIGTERM).
  AppLifecycleListener(
    onExitRequested: () async {
      try {
        await container.read(connectionProvider.notifier).disconnect();
      } catch (_) {}
      return AppExitResponse.exit;
    },
  );

  // Auto-connect only when there's something to connect to; otherwise the
  // connect gate guides first-run setup.
  final settings = container.read(settingsProvider);
  final configured = settings.mode == ConnectionMode.remote
      ? settings.remoteUrl.trim().isNotEmpty
      : settings.workingDirectory.trim().isNotEmpty;
  if (configured) {
    unawaited(container.read(connectionProvider.notifier).connect());
  }

  // Deferred to after the first frame: window_manager/tray_manager need the
  // native window fully attached, which isn't guaranteed yet at this point
  // in main() — starting early enough could hit it mid-setup.
  WidgetsBinding.instance.addPostFrameCallback((_) {
    unawaited(TrayController.instance.start());
  });

  runApp(
    UncontrolledProviderScope(container: container, child: const GoCodeApp()),
  );
}

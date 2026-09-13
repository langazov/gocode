import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/app.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:integration_test/integration_test.dart';

/// Optional: `--dart-define=SHOT_DIR=/some/dir` saves a PNG per step.
const _shotDir = String.fromEnvironment('SHOT_DIR');

/// End to end through the real UI: the supervisor spawns `gocode serve`,
/// models load, a session is created, and the app lands in it.
///
/// Needs gocode on the login-shell PATH (or a common install location).
void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('starts the server, loads models, and starts a session', (
    tester,
  ) async {
    final project = await Directory.systemTemp.createTemp('gocode_gui_flow_');
    final container = ProviderContainer();
    final shotKey = GlobalKey();
    addTearDown(() async {
      await container.read(connectionProvider.notifier).disconnect();
      container.dispose();
      await project.delete(recursive: true);
    });

    await tester.pumpWidget(
      RepaintBoundary(
        key: shotKey,
        child: UncontrolledProviderScope(
          container: container,
          child: const GoCodeApp(),
        ),
      ),
    );

    Future<void> until(bool Function() done, String what) async {
      final end = DateTime.now().add(const Duration(seconds: 60));
      while (DateTime.now().isBefore(end)) {
        await tester.pump(const Duration(milliseconds: 100));
        if (done()) return;
      }
      fail('timed out waiting for $what');
    }

    Future<void> untilFound(Finder finder) =>
        until(() => finder.evaluate().isNotEmpty, '$finder');

    Future<void> shot(String name) async {
      if (_shotDir.isEmpty) return;
      await tester.pump(const Duration(milliseconds: 400));
      final boundary =
          shotKey.currentContext!.findRenderObject()! as RenderRepaintBoundary;
      final image = await boundary.toImage(pixelRatio: 2);
      final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
      await File('$_shotDir/$name.png')
          .writeAsBytes(bytes!.buffer.asUint8List());
    }

    await untilFound(find.text('Choose project folder'));
    await shot('1-connect');

    // Local mode, as after "Choose project folder": spawn gocode there.
    await container
        .read(connectionProvider.notifier)
        .apply(ConnectionSettings(workingDirectory: project.path));
    expect(container.read(connectionProvider).error, isNull);
    await untilFound(find.text('Sessions'));
    expect(container.read(apiClientProvider), isNotNull);
    await shot('2-home');

    await tester.tap(find.text('New session'));
    // "Server default" renders once the model list has loaded.
    await untilFound(find.text('Server default'));
    // Settings weren't persisted here (that would touch the real app's
    // preferences), so type the directory like a user would.
    await tester.enterText(
      find.widgetWithText(TextField, '/path/to/project'),
      project.path,
    );
    await tester.pump();
    await shot('3-new-session');

    await tester.tap(find.text('Server default'));
    await untilFound(find.text('Choose a model'));
    await shot('4-model-picker');
    await tester.tapAt(const Offset(4, 4)); // the barrier
    await until(
      () => find.text('Choose a model').evaluate().isEmpty,
      'the picker to close',
    );

    final start = find.text('Start session');
    await tester.ensureVisible(start);
    await tester.pump(const Duration(milliseconds: 200));
    await tester.tap(start);
    await untilFound(find.text('What are we building?'));

    // The session list is gocode's global history, not just this project.
    final client = container.read(apiClientProvider)!;
    final created = (await client.sessions())
        .where((s) => s.directory == project.path)
        .toList();
    expect(created, hasLength(1));

    await tester.enterText(find.byType(TextField).last, 'Add a README');
    await shot('5-session');

    // Leave the user's session history as we found it.
    await client.deleteSession(created.single.id);
  });
}

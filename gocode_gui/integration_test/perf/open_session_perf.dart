import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/app.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:integration_test/integration_test.dart';

/// Frame timings for opening a session: the route transition plus the first
/// render of its timeline. Only meaningful in profile mode:
///
///   flutter drive --profile -d macos \
///     --driver=test_driver/perf_driver.dart \
///     --target=integration_test/perf/open_session_perf.dart
///
/// Opens the busiest recent session in the local gocode history (read-only).
/// Not named *_test.dart, so CI's integration loop doesn't pick it up.
void main() {
  final binding = IntegrationTestWidgetsFlutterBinding.ensureInitialized();
  // Real engine frames, as a user sees them, rather than test-pumped ones.
  binding.framePolicy = LiveTestWidgetsFlutterBindingFramePolicy.fullyLive;

  testWidgets('open a session', (tester) async {
    final project = await Directory.systemTemp.createTemp(
      'gocode_desktop_perf_',
    );
    final container = ProviderContainer();
    addTearDown(() async {
      await container.read(connectionProvider.notifier).disconnect();
      container.dispose();
      await project.delete(recursive: true);
    });

    await tester.pumpWidget(
      UncontrolledProviderScope(container: container, child: const GoCodeApp()),
    );
    await container
        .read(connectionProvider.notifier)
        .apply(ConnectionSettings(workingDirectory: project.path));
    expect(container.read(connectionProvider).isConnected, isTrue);

    final client = container.read(apiClientProvider)!;
    final recent = (await client.sessions())
      ..sort((a, b) => b.timeUpdated.compareTo(a.timeUpdated));
    Session? target;
    var most = 0;
    for (final s in recent.take(20)) {
      final count = (await client.messages(s.id)).length;
      if (count > most) {
        most = count;
        target = s;
      }
    }
    if (target == null) {
      debugPrint('perf: no session with messages in the local history');
      return;
    }
    debugPrint('perf: opening "${target.title}" ($most messages)');

    final router = container.read(routerProvider);
    final route = '/session/${target.id}';
    // Let home finish loading so it isn't part of the measurement.
    await Future<void>.delayed(const Duration(seconds: 1));

    await binding.watchPerformance(() async {
      for (var i = 0; i < 3; i++) {
        router.push(route);
        await Future<void>.delayed(const Duration(milliseconds: 1500));
        router.pop();
        await Future<void>.delayed(const Duration(milliseconds: 800));
      }
    }, reportKey: 'open_session');
  });
}

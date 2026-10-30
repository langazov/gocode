import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/client.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/harness/harness_screen.dart';
import 'package:gocode_gui/features/harness/harness_widgets.dart';
import 'package:gocode_gui/features/sidebar/sidebar.dart';
import 'package:gocode_gui/features/sidebar/projects.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

Map<String, dynamic> _run(String id, String status) => {
  'id': id,
  'task': id.split('--').first,
  'prompt': 'Do ${id.split('--').first}',
  'domain': 'auth',
  'k': 1,
  'mode': 'parallel',
  'status': status,
  'phase': status == 'running' ? 'probe' : '',
  'rows': [
    {'label': 'base1', 'phase': 'base', 'state': 'ok', 'costUSD': 0.4},
    {'label': 'inv1', 'phase': 'probe', 'state': 'pending', 'costUSD': 0},
  ],
  'costUSD': 0.4,
  'startedAt': '2025-06-01T10:00:00Z',
  'budget': 'parallel k=1 → 4 rollouts, 3 editor sessions',
  'adopted': 'agg1',
  'workspace': '/project',
};

/// A scripted /api/harness server in the _FakeGit style: serves a run list
/// and records every request.
class _FakeHarness {
  final requests = <http.Request>[];

  List<Map<String, dynamic>> runs = [
    _run('fix-login--k1-parallel', 'running'),
    _run('write-docs--k2-sequential', 'ok'),
  ];

  MockClient get client => MockClient((request) async {
    requests.add(request);
    final path = request.url.path;
    Object? body = switch (path) {
      '/api/harness/run' => runs,
      '/api/harness/budgets' => [
        {
          'mode': 'parallel',
          'k': 1,
          'rollouts': 4,
          'edits': 3,
          'description': 'one Propose–Probe–Compose step',
        },
      ],
      _ => null,
    };
    if (body == null) {
      return http.Response('{"error":"no route"}', 404);
    }
    return http.Response(
      jsonEncode(body),
      200,
      headers: {'content-type': 'application/json'},
    );
  });
}

class _NoProjects extends ProjectsNotifier {
  @override
  List<Project> build() => const [];
}

Future<_FakeHarness> _pump(WidgetTester tester) async {
  final fake = _FakeHarness();
  final client = GocodeClient(
    baseUrl: 'http://gocode.test',
    httpClient: fake.client,
  );
  tester.view.physicalSize = const Size(1400, 1000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(client),
        projectsProvider.overrideWith(_NoProjects.new),
      ],
      child: MaterialApp(theme: AppTheme.dark(), home: const HarnessScreen()),
    ),
  );
  await tester.pumpAndSettle();
  return fake;
}

Future<void> _unmount(WidgetTester tester) async {
  await tester.pumpWidget(const SizedBox.shrink());
}

void main() {
  SharedPreferences.setMockInitialValues({});

  testWidgets('lists running above finished with their details', (tester) async {
    await _pump(tester);

    // The caption and the running card's status chip both say it.
    expect(find.text('RUNNING'), findsNWidgets(2));
    expect(find.text('FINISHED'), findsOneWidget);
    expect(find.text('fix-login'), findsOneWidget);
    expect(find.text('write-docs'), findsOneWidget);
    // Prompt excerpt and cost.
    expect(find.text('Do fix-login'), findsOneWidget);
    expect(find.text(r'$0.40'), findsNWidgets(2));
    // The running card's phase progress.
    expect(find.textContaining('1/2'), findsOneWidget);
    await _unmount(tester);
  });

  testWidgets('an empty list offers the start flow', (tester) async {
    final fake = await _pump(tester);
    fake.runs = [];
    await tester.tap(find.byKey(HarnessKeys.refresh));
    await tester.pumpAndSettle();

    expect(find.text('No harness runs yet'), findsOneWidget);
    expect(find.byKey(HarnessKeys.newRun), findsNWidgets(2)); // header + empty
    await _unmount(tester);
  });

  testWidgets('the ACP gate explains the mode requirement', (tester) async {
    tester.view.physicalSize = const Size(1400, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [projectsProvider.overrideWith(_NoProjects.new)],
        child: MaterialApp(theme: AppTheme.dark(), home: const HarnessScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Harness needs Server or Remote mode'), findsOneWidget);
    expect(find.text('Open Settings'), findsOneWidget);
    await _unmount(tester);
  });

  testWidgets('the sidebar harness row hides in ACP mode', (tester) async {
    // ACP mode: no HTTP client — the row is gone.
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [projectsProvider.overrideWith(_NoProjects.new)],
        child: MaterialApp(
          theme: AppTheme.dark(),
          home: Row(
            children: const [
              SizedBox(width: 256, child: Sidebar(location: '/harness')),
            ],
          ),
        ),
      ),
    );
    // Live dot animations run forever, so settle-by-frame, not pumpAndSettle.
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.byKey(SidebarKeys.harness), findsNothing);
    await tester.pumpWidget(const SizedBox.shrink());

    // Server mode: the row appears.
    final fake = _FakeHarness();
    final client = GocodeClient(
      baseUrl: 'http://gocode.test',
      httpClient: fake.client,
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(client),
          projectsProvider.overrideWith(_NoProjects.new),
        ],
        child: MaterialApp(
          theme: AppTheme.dark(),
          home: Row(
            children: const [
              SizedBox(width: 256, child: Sidebar(location: '/harness')),
            ],
          ),
        ),
      ),
    );
    // The live run's pulsing dot never settles; advance frames instead.
    await tester.pump(const Duration(milliseconds: 100));
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.byKey(SidebarKeys.harness), findsOneWidget);
    expect(find.text('Harness'), findsOneWidget);
    await _unmount(tester);
  });

  testWidgets('delete sits behind its confirm dialog', (tester) async {
    final fake = await _pump(tester);

    // The finished card's menu offers Delete.
    await tester.tap(find.byTooltip('Run actions').last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Delete').last);
    await tester.pumpAndSettle();

    expect(find.textContaining('Delete write-docs?'), findsOneWidget);
    expect(find.textContaining('experience library is kept'), findsOneWidget);

    // Cancel leaves the run in place; no DELETE request was sent.
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(
      fake.requests.where((r) => r.method == 'DELETE'),
      isEmpty,
      reason: 'confirming is what deletes',
    );
    await _unmount(tester);
  });

}

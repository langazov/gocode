import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/client.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/harness/harness_start_dialog.dart';
import 'package:gocode_gui/features/sidebar/projects.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// The start flow (documentation/11-desktop.md, Harness²): a run is never
/// started without its price on screen. The fake records every request, so
/// the test asserts the plan came first and only Start spends.
/// http.Response encodes a String body as ascii; Harness² payloads carry
/// '→' and '–', so the fakes respond in utf-8 bytes.
http.Response _ok200(Object body, [int status = 200]) =>
    http.Response.bytes(utf8.encode(jsonEncode(body)), status);

class _FakeHarness {
  final requests = <http.Request>[];
  int starts = 0;
  bool alreadyRunning = false;

  Map<String, dynamic> _plan(Map<String, dynamic> body) => {
    'id': '${body['id'] ?? 'task'}--k${body['k'] ?? 1}-${body['mode'] ?? 'parallel'}',
    'task': body['id'] ?? 'task',
    'domain': body['domain'] ?? 'server',
    'k': body['k'] ?? 1,
    'mode': body['mode'] ?? 'parallel',
    'status': 'planned',
    'backend': 'local',
    'reason': 'harness.library is local',
    'rows': [
      {'label': 'base1', 'phase': 'base', 'state': 'pending', 'costUSD': 0},
      {'label': 'inv1', 'phase': 'probe', 'state': 'pending', 'costUSD': 0},
      {'label': 'dom1', 'phase': 'probe', 'state': 'pending', 'costUSD': 0},
      {'label': 'agg1', 'phase': 'compose', 'state': 'pending', 'costUSD': 0},
      {'label': 'experience', 'phase': 'reflect', 'state': 'pending', 'costUSD': 0},
    ],
    'costUSD': 0,
    'startedAt': '2025-06-01T10:00:00Z',
    'budget': '${body['mode'] ?? 'parallel'} k=${body['k'] ?? 1} → '
        '4 rollouts, 3 editor sessions + 1 reflector',
    'workspace': body['workspace'] ?? '',
    'prompt': body['prompt'],
  };

  MockClient get client => MockClient((request) async {
    requests.add(request);
    final path = request.url.path;
    if (path == '/api/harness/budgets') {
      return _ok200([
        {
          'mode': 'parallel',
          'k': 1,
          'rollouts': 4,
          'edits': 3,
          'description': 'one Propose–Probe–Compose step',
        },
        {
          'mode': 'sequential',
          'k': 2,
          'rollouts': 8,
          'edits': 6,
          'description': 'deeper: 2 rounds, each building on the last',
        },
      ]);
    }
    if (path == '/api/harness/run' && request.method == 'POST') {
      final body = jsonDecode(request.body) as Map<String, dynamic>;
      if (body['dryRun'] == true) {
        return _ok200(_plan(body));
      }
      starts++;
      if (alreadyRunning) {
        return _ok200({'error': 'this run is already in progress'}, 409);
      }
      return _ok200({..._plan(body), 'status': 'running'}, 202);
    }
    if (path == '/api/model') {
      return _ok200([]);
    }
    return http.Response('{"error":"no route"}', 404);
  });

  List<Map<String, dynamic>> posted() => [
    for (final r in requests)
      if (r.url.path == '/api/harness/run' && r.method == 'POST')
        jsonDecode(r.body) as Map<String, dynamic>,
  ];
}

class _NoProjects extends ProjectsNotifier {
  @override
  List<Project> build() => const [];
}

Future<_FakeHarness> _pump(
  WidgetTester tester, {
  String initialPrompt = '',
  String? workspace,
}) async {
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
      child: MaterialApp(
        theme: AppTheme.dark(),
        home: Scaffold(
          body: Builder(
            builder: (context) => FilledButton(
              onPressed: () => showHarnessStartDialog(
                context,
                prompt: initialPrompt,
                workspace: workspace,
              ),
              child: const Text('Open'),
            ),
          ),
        ),
      ),
    ),
  );
  await tester.pump();
  await tester.tap(find.text('Open'));
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 300));
  return fake;
}

void main() {
  SharedPreferences.setMockInitialValues({});

  testWidgets('dry-runs first and spends only on Start', (tester) async {
    final fake = await _pump(
      tester,
      initialPrompt: 'Fix the login redirect',
      workspace: '/project',
    );

    // Step 1: the task is prefilled; the id seeds from its slug.
    expect(find.text('Fix the login redirect'), findsOneWidget);
    expect(fake.posted(), isEmpty);

    await tester.tap(find.text('Next'));
    await tester.pump();

    // Step 2: the budgets and their price.
    expect(find.text('parallel k=1'), findsOneWidget);
    expect(find.text('4 rollouts · 3 edits'), findsOneWidget);
    expect(fake.posted(), isEmpty, reason: 'planning comes after the budget');

    await tester.tap(find.text('Plan'));
    await tester.pumpAndSettle();

    // Step 3: the plan arrived — one POST, and it was the dry run.
    expect(fake.posted().single['dryRun'], isTrue);
    expect(find.textContaining('4 rollouts'), findsOneWidget);
    expect(find.textContaining('local'), findsWidgets);

    // Start is the only button that spends.
    await tester.tap(find.text('Start'));
    await tester.pumpAndSettle();
    expect(fake.starts, 1);
    final posts = fake.posted();
    expect(posts.length, 2);
    expect(posts.last['dryRun'], isNull);
    expect(posts.last['prompt'], 'Fix the login redirect');
    expect(posts.last['mode'], 'parallel');
    expect(posts.last['k'], 1);
    expect(posts.last['workspace'], '/project');
    // The dialog closed.
    expect(find.text('New harness run'), findsNothing);
  });

  testWidgets('⌘↵ runs the current primary action', (tester) async {
    final fake = await _pump(tester, initialPrompt: 'Write the docs');

    // Step 1: ⌘↵ advances.
    await tester.sendKeyDownEvent(LogicalKeyboardKey.meta);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.meta);
    await tester.pump();
    expect(find.text('parallel k=1'), findsOneWidget);
    expect(fake.posted(), isEmpty);
  });

  testWidgets('a 409 offers Open run instead', (tester) async {
    final fake = await _pump(tester, initialPrompt: 'Fix the login redirect');
    fake.alreadyRunning = true;

    await tester.tap(find.text('Next'));
    await tester.pump();
    await tester.tap(find.text('Plan'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Start'));
    await tester.pumpAndSettle();

    expect(fake.starts, 1);
    expect(find.byKey(const ValueKey('harness-open-run')), findsOneWidget);
    expect(find.textContaining('already in progress'), findsOneWidget);
  });

  test('the slug mirrors the server', () {
    expect(harnessSlug('Fix the login redirect!'), 'fix-the-login-redirect');
    expect(harnessSlug('  '), 'task');
    expect(harnessSlug('A/B C'), 'a-b-c');
    final long = List.filled(100, 'word').join(' ');
    expect(harnessSlug(long).length, lessThanOrEqualTo(48));
  });
}

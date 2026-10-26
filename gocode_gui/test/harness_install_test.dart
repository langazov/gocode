import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/client.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/harness/harness_install_dialog.dart';
import 'package:gocode_gui/features/sidebar/projects.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// The install flow (documentation/HARNESS2_GUI.md §5.6): always previewed
/// first — dry-run, show, then write. Records every request so the test
/// asserts dryRun came first and force only after the conflict was shown.
class _FakeHarness {
  final requests = <http.Request>[];
  bool conflict = true;
  bool unchanged = false;

  http.Response _ok(Object body, [int status = 200]) =>
      http.Response.bytes(utf8.encode(jsonEncode(body)), status);

  Map<String, dynamic> _installPlan() => {
    'target': '/project/.gocode/harness',
    'files': {
      'added': ['systemprompt.md'],
      'removed': <String>[],
      'modified': <String>[],
      'alwaysOnCharsDelta': 42,
    },
    'conflict': conflict,
    'collisions': ['a-skill.md'],
    'warnings': <String>[],
    'written': false,
  };

  MockClient get client => MockClient((request) async {
    requests.add(request);
    if (request.url.path.contains('/install')) {
      final body = jsonDecode(request.body) as Map<String, dynamic>;
      final dryRun = body['dryRun'] == true;
      if (dryRun) {
        if (unchanged) {
          return _ok({
            'target': '/project/.gocode/harness',
            'files': {
              'added': <String>[],
              'removed': <String>[],
              'modified': <String>[],
              'alwaysOnCharsDelta': 0,
            },
            'conflict': false,
            'written': false,
          });
        }
        return _ok(_installPlan());
      }
      // A real install without force over a conflict is a 409 carrying the
      // result — the client maps it to HarnessInstallConflict.
      if (conflict && body['force'] != true) {
        return _ok({
          'error': '/project/.gocode/harness already holds a different '
              'harness; replace it with force',
          'result': _installPlan(),
        }, 409);
      }
      return _ok({..._installPlan(), 'conflict': false, 'written': true});
    }
    if (request.url.path == '/api/harness/run') {
      return _ok([]);
    }
    return _ok({'error': 'no route'}, 404);
  });

  List<Map<String, dynamic>> posts() => [
    for (final r in requests)
      if (r.url.path.contains('/install') && r.method == 'POST')
        jsonDecode(r.body) as Map<String, dynamic>,
  ];
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
      child: MaterialApp(
        theme: AppTheme.dark(),
        home: Scaffold(
          body: Builder(
            builder: (context) => FilledButton(
              onPressed: () => showHarnessInstallDialog(
                context,
                runID: 'fix-login--k1-parallel',
                to: 'project',
                dir: '/project',
              ),
              child: const Text('Open'),
            ),
          ),
        ),
      ),
    ),
  );
  await tester.pump();
  return fake;
}

Future<_FakeHarness> _open(WidgetTester tester) async {
  final fake = await _pump(tester);
  await tester.tap(find.text('Open'));
  return fake;
}

void main() {
  SharedPreferences.setMockInitialValues({});

  testWidgets('dry-runs first; force only after the conflict was shown', (
    tester,
  ) async {
    final fake = await _open(tester);
    await tester.pumpAndSettle();

    // The preview: the conflict was spelled out before anything was written.
    expect(find.textContaining('Replaces the harness installed there now.'), findsOneWidget);
    expect(find.textContaining('a-skill.md keep'), findsOneWidget);
    expect(fake.posts().single['dryRun'], isTrue);

    // The confirm button names the replacement.
    final replace = find.text('Replace and install');
    expect(replace, findsOneWidget);
    await tester.tap(replace);
    await tester.pumpAndSettle();

    // The real install: force follows the shown conflict.
    final posts = fake.posts();
    expect(posts.length, 2);
    expect(posts.last['dryRun'], isNot(isTrue));
    expect(posts.last['force'], isTrue);
    expect(posts.last['dir'], '/project');
    // The result was announced.
    expect(find.textContaining('Installed into'), findsOneWidget);
  });

  testWidgets('"already holds this harness" short-circuits to a SnackBar', (
    tester,
  ) async {
    final fake = await _pump(tester);
    fake.unchanged = true;
    await tester.tap(find.text('Open'));
    await tester.pumpAndSettle();

    // No dialog: the SnackBar said it directly (the TUI's behaviour).
    expect(find.text('Install harness?'), findsNothing);
    expect(find.textContaining('already holds this harness'), findsOneWidget);
    expect(fake.posts().single['dryRun'], isTrue);
  });
}

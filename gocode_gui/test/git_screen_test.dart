import 'dart:convert';

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/client.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/git/git_screen.dart';
import 'package:gocode_gui/features/git/git_widgets.dart' show promptText;
import 'package:gocode_gui/features/sidebar/projects.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

const _diff = '''diff --git a/lib/a.dart b/lib/a.dart
index 1111111..2222222 100644
--- a/lib/a.dart
+++ b/lib/a.dart
@@ -1,2 +1,2 @@
-old line
+new line
 kept
''';

/// A scripted /api/vcs/git server: one modified and one untracked file;
/// records every request.
class _FakeGit {
  final requests = <http.Request>[];

  Map<String, Object?> get status => {
    'isRepo': true,
    'root': '/repo',
    'branch': 'main',
    'head': 'abc1234',
    'hasCommits': true,
    'remotes': <String>[],
    'files': [
      {'path': 'lib/a.dart', 'status': 'M', 'staged': false},
      {'path': 'notes.txt', 'status': '?', 'staged': false},
    ],
  };

  MockClient get client => MockClient((request) async {
    requests.add(request);
    final path = request.url.path;
    if (path == '/api/vcs/git/commit-message') {
      final lines = [
        {'text': 'Update a', 'model': 'openai/gpt-mini'},
        {
          'text': 'Update a.dart line',
          'model': 'openai/gpt-mini',
          'done': true,
        },
      ];
      return http.Response(
        lines.map(jsonEncode).join('\n'),
        200,
        headers: {'content-type': 'application/x-ndjson'},
      );
    }
    Object? body = switch (path) {
      '/api/vcs/git/status' => status,
      '/api/vcs/git/diff' => {'diff': _diff},
      '/api/vcs/git/stash' => {'entries': <Object>[]},
      '/api/vcs/git/log' => {'commits': <Object>[]},
      _ when request.method == 'POST' => <String, Object>{},
      _ => null,
    };
    if (body == null) return http.Response('{"error":"no route"}', 404);
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

Future<_FakeGit> _pump(WidgetTester tester, {String? directory}) async {
  final fake = _FakeGit();
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
        home: GitScreen(directory: directory),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return fake;
}

/// Unmounts the screen so the status poll's timer is cancelled.
Future<void> _unmount(WidgetTester tester) async {
  await tester.pumpWidget(const SizedBox.shrink());
}

void main() {
  SharedPreferences.setMockInitialValues({});

  testWidgets('lists the branch and changed files of the routed folder', (
    tester,
  ) async {
    final fake = await _pump(tester, directory: '/repo');

    expect(find.text('main'), findsOneWidget);
    expect(find.textContaining('a.dart', findRichText: true), findsOneWidget);
    expect(
      find.textContaining('notes.txt', findRichText: true),
      findsOneWidget,
    );
    expect(find.text('Commit All'), findsOneWidget);
    final status = fake.requests.firstWhere(
      (r) => r.url.path == '/api/vcs/git/status',
    );
    expect(status.url.queryParameters['directory'], '/repo');
    await _unmount(tester);
  });

  testWidgets('stage action posts the file path', (tester) async {
    final fake = await _pump(tester);

    final row = find.textContaining('a.dart', findRichText: true);
    final gesture = await tester.createGesture(kind: PointerDeviceKind.mouse);
    await gesture.addPointer(location: tester.getCenter(row));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Stage').first);
    await tester.pumpAndSettle();

    final post = fake.requests.lastWhere((r) => r.method == 'POST');
    expect(post.url.path, '/api/vcs/git/stage');
    expect(jsonDecode(post.body), {
      'paths': ['lib/a.dart'],
    });
    await gesture.removePointer();
    await _unmount(tester);
  });

  testWidgets('opens the diff viewer with inline and side-by-side views', (
    tester,
  ) async {
    await _pump(tester);

    await tester.tap(find.textContaining('a.dart', findRichText: true));
    await tester.pumpAndSettle();

    expect(find.text('Working Tree'), findsOneWidget);
    expect(find.text('old line'), findsOneWidget);
    expect(find.text('new line'), findsOneWidget);
    expect(find.text('Stage Hunk'), findsOneWidget);

    await tester.tap(find.text('Side by side'));
    await tester.pumpAndSettle();
    // Context lines show on both sides.
    expect(find.text('kept'), findsNWidgets(2));
    await _unmount(tester);
  });

  testWidgets('without an HTTP client explains the mode', (tester) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(null),
          projectsProvider.overrideWith(_NoProjects.new),
        ],
        child: MaterialApp(theme: AppTheme.dark(), home: const GitScreen()),
      ),
    );
    await tester.pumpAndSettle();
    expect(
      find.text('Source Control needs Server or Remote mode'),
      findsOneWidget,
    );
    await _unmount(tester);
  });

  testWidgets('drafts a commit message with AI', (tester) async {
    final fake = await _pump(tester);

    await tester.tap(find.byIcon(Icons.auto_awesome_rounded));
    await tester.pumpAndSettle();

    expect(find.text('Update a.dart line'), findsOneWidget);
    expect(
      find.text('Written by openai/gpt-mini — review before committing'),
      findsOneWidget,
    );
    final post = fake.requests.lastWhere(
      (r) => r.url.path == '/api/vcs/git/commit-message',
    );
    // Nothing staged: the draft describes everything Commit All takes.
    expect(jsonDecode(post.body), {'stagedOnly': false});
    await _unmount(tester);
  });

  testWidgets('text prompt survives its closing animation', (tester) async {
    // Regression: the prompt's controller was disposed as soon as
    // showDialog returned, while the field was still animating out.
    String? result;
    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.dark(),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () async =>
                result = await promptText(context, title: 'New branch'),
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'feature/x');
    await tester.tap(find.text('OK'));
    await tester.pumpAndSettle();

    expect(result, 'feature/x');
    expect(tester.takeException(), isNull);
  });
}

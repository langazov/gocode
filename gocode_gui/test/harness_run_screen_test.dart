import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/client.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/harness/harness_activity.dart';
import 'package:gocode_gui/features/harness/harness_run_screen.dart';
import 'package:gocode_gui/features/harness/harness_widgets.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

const _id = 'fix-login--k1-parallel';

final _run = {
  'id': _id,
  'task': 'fix-login',
  'domain': 'auth',
  'k': 1,
  'mode': 'parallel',
  'status': 'running',
  'phase': 'probe',
  'rows': [
    {
      'label': 'base1',
      'phase': 'base',
      'state': 'failed',
      'costUSD': 0,
      'sessionID': 'ses_base',
      'detail': 'stopped after 15m0s without activity: the session stalled',
    },
    {
      'label': 'dom1',
      'phase': 'probe',
      'state': 'rolling',
      'costUSD': 0,
      'sessionID': 'ses_roll',
      'editSessionID': 'ses_edit',
    },
  ],
  'costUSD': 0,
  'startedAt': DateTime.now()
      .subtract(const Duration(minutes: 3))
      .toUtc()
      .toIso8601String(),
};

final _log = [
  {'type': 'phase', 'time': 1000, 'task': 'fix-login', 'phase': 'base'},
  {
    'type': 'rollout.ended',
    'time': 61000,
    'task': 'fix-login',
    'label': 'base1',
    'ok': false,
    'detail': 'stopped after 15m0s without activity: the session stalled',
  },
];

/// Fixed activity, as if ses_roll were mid-bash.
class _Activity extends HarnessActivityNotifier {
  @override
  Map<String, HarnessSessionActivity> build() => {
    'ses_roll': HarnessSessionActivity(
      lastEventAt: DateTime.now(),
      tool: 'bash',
      toolInput: 'go test ./...',
      tools: 7,
      steps: 4,
    ),
  };
}

Future<void> _pump(WidgetTester tester) async {
  final client = GocodeClient(
    baseUrl: 'http://gocode.test',
    httpClient: MockClient((request) async {
      final body = switch (request.url.path) {
        '/api/harness/run' => [_run],
        '/api/harness/run/$_id/log' => _log,
        _ => null,
      };
      return body == null
          ? http.Response('{"error":"no route"}', 404)
          : http.Response(
              jsonEncode(body),
              200,
              headers: {'content-type': 'application/json'},
            );
    }),
  );
  tester.view.physicalSize = const Size(1400, 1100);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(client),
        harnessActivityProvider.overrideWith(_Activity.new),
      ],
      child: MaterialApp(
        theme: AppTheme.dark(),
        home: const HarnessRunScreen(id: _id),
      ),
    ),
  );
  // Ticking clocks never settle; pump a few frames instead.
  for (var i = 0; i < 5; i++) {
    await tester.pump(const Duration(milliseconds: 50));
  }
}

void main() {
  testWidgets('rows show what is running, how long, and where to watch', (
    tester,
  ) async {
    await _pump(tester);

    // The run's elapsed time ticks in the header.
    expect(find.textContaining(RegExp(r'^3m \d\ds$')), findsOneWidget);
    // The rolling row's live line: tool, input, counts.
    expect(find.textContaining('bash · go test ./...'), findsOneWidget);
    expect(find.textContaining('4 steps · 7 tools'), findsOneWidget);
    // Watch buttons: two rollouts and one editor.
    expect(find.byTooltip('Watch the rollout live'), findsOneWidget);
    expect(find.byTooltip('Open the rollout session'), findsOneWidget);
    expect(find.byTooltip('Open the editor session'), findsOneWidget);
    // A failed row says why.
    expect(find.textContaining('the session stalled'), findsOneWidget);

    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('the event log opens on demand', (tester) async {
    await _pump(tester);
    expect(find.text('Event log'), findsOneWidget);
    expect(find.textContaining('rollout.ended'), findsNothing);

    await tester.tap(find.byKey(HarnessKeys.eventLog));
    for (var i = 0; i < 5; i++) {
      await tester.pump(const Duration(milliseconds: 50));
    }
    expect(find.text('FOLLOWING'), findsOneWidget);
    expect(
      find.textContaining('rollout.ended · base1 · failed'),
      findsOneWidget,
    );
    expect(find.text('1m 00s'), findsOneWidget); // offset from the first line

    await tester.pumpWidget(const SizedBox.shrink());
  });
}

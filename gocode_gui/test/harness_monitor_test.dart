import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_riverpod/misc.dart' show Override;
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/harness/harness_activity.dart';
import 'package:gocode_gui/features/harness/harness_state.dart';
import 'package:gocode_gui/features/session/harness_notice.dart';
import 'package:gocode_gui/features/session/timeline.dart';

/// A stand-in gocode server for live harness monitoring: the runs list, a
/// run's event log, and an SSE stream the test writes session events into.
class _FakeServer {
  _FakeServer(this._server);

  final HttpServer _server;
  final _sse = <HttpResponse>[];
  List<Map<String, Object?>> runs = [];
  List<Map<String, Object?>> log = [];
  final logReads = <int>[];

  int get port => _server.port;

  static Future<_FakeServer> start() async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final fake = _FakeServer(server);
    server.listen(fake._handle);
    return fake;
  }

  /// Pushes a durable session event the way the server frames it: the
  /// session ID copied up from the data.
  Future<void> emitSessionEvent(
    String sessionID,
    String type, [
    Map? data,
  ]) async {
    final event = jsonEncode({
      'id': 'e${DateTime.now().microsecondsSinceEpoch}',
      'type': type,
      'sessionID': sessionID,
      'data': {'sessionID': sessionID, ...?data},
    });
    for (final response in _sse.toList()) {
      response.add(utf8.encode('data: $event\n\n'));
      await response.flush();
    }
  }

  Future<void> emitRun(Map<String, Object?> run) async {
    final event = jsonEncode({
      'id': 'h${DateTime.now().microsecondsSinceEpoch}',
      'type': 'harness.phase',
      'data': {
        'run': run,
        'event': {'type': 'phase'},
      },
    });
    for (final response in _sse.toList()) {
      response.add(utf8.encode('data: $event\n\n'));
      await response.flush();
    }
  }

  Future<void> close() => _server.close(force: true);

  void _json(HttpRequest request, Object body) {
    request.response
      ..headers.contentType = ContentType.json
      ..write(jsonEncode(body));
    unawaited(request.response.close());
  }

  void _handle(HttpRequest request) {
    final path = request.uri.path;
    if (path == '/api/health') return _json(request, {'healthy': true});
    if (path == '/api/harness/run') return _json(request, runs);
    if (path.endsWith('/log')) {
      final after = int.tryParse(request.uri.queryParameters['after'] ?? '');
      logReads.add(after ?? 0);
      return _json(request, log.skip(after ?? 0).toList());
    }
    if (path == '/api/event') {
      _sse.add(request.response);
      request.response
        ..headers.contentType = ContentType('text', 'event-stream')
        ..bufferOutput = false
        ..write(': ok\n\n');
      unawaited(request.response.flush());
      return;
    }
    request.response.statusCode = 404;
    unawaited(request.response.close());
  }
}

const _runID = 'fix-login--k1-parallel';

Map<String, Object?> _run(String status) => {
  'id': _runID,
  'task': 'fix-login',
  'domain': 'auth',
  'k': 1,
  'mode': 'parallel',
  'status': status,
  'phase': 'probe',
  'rows': [
    {
      'label': 'base1',
      'phase': 'base',
      'state': 'rolling',
      'costUSD': 0,
      'sessionID': 'ses_roll',
    },
    {
      'label': 'inv1',
      'phase': 'probe',
      'state': 'editing',
      'costUSD': 0,
      'editSessionID': 'ses_edit',
    },
  ],
  'costUSD': 0,
  'startedAt': '2025-06-01T10:00:00Z',
};

Map<String, Object?> _logLine(String type, {String label = ''}) => {
  'type': type,
  'time': 1748772000000,
  'task': 'fix-login',
  'label': label,
  'ok': true,
};

Future<void> _pumpEvents() async {
  for (var i = 0; i < 20; i++) {
    await Future<void>.delayed(const Duration(milliseconds: 5));
  }
}

Future<(_FakeServer, ProviderContainer)> _connect({
  List<Override> overrides = const [],
}) async {
  final fake = await _FakeServer.start();
  final container = ProviderContainer(overrides: overrides);
  addTearDown(() async {
    container.dispose();
    await fake.close();
  });
  fake.runs = [_run('running')];
  container.listen(harnessRunsProvider, (_, _) {});
  await container
      .read(connectionProvider.notifier)
      .apply(
        ConnectionSettings(
          mode: ConnectionMode.remote,
          remoteUrl: 'http://127.0.0.1:${fake.port}',
        ),
      );
  await _pumpEvents();
  return (fake, container);
}

void main() {
  test('activity follows the running rows\' sessions and only those', () async {
    final (fake, container) = await _connect();
    final activity = container.listen(harnessActivityProvider, (_, _) {});

    await fake.emitSessionEvent('ses_roll', 'session.next.step.started');
    await fake.emitSessionEvent('ses_roll', 'session.next.tool.called', {
      'tool': 'bash',
      'input': {'command': 'go   test ./...'},
    });
    await fake.emitSessionEvent('ses_edit', 'session.next.step.started');
    // A session no harness row owns is not tracked.
    await fake.emitSessionEvent('ses_other', 'session.next.step.started');
    await _pumpEvents();

    final roll = activity.read()['ses_roll']!;
    expect(roll.tool, 'bash');
    expect(roll.toolInput, 'go test ./...');
    expect((roll.steps, roll.tools), (1, 1));
    expect(activity.read()['ses_edit']!.steps, 1);
    expect(activity.read().containsKey('ses_other'), isFalse);

    await fake.emitSessionEvent('ses_roll', 'session.next.tool.failed');
    await _pumpEvents();
    expect(activity.read()['ses_roll']!.tool, isNull);
    expect(activity.read()['ses_roll']!.failedTools, 1);

    // Once the run stops, its sessions are forgotten.
    await fake.emitRun(_run('ok'));
    await _pumpEvents();
    expect(activity.read(), isEmpty);
  });

  test('a running run\'s log is followed from the last line held', () async {
    final (fake, container) = await _connect(
      overrides: [
        harnessLogIntervalProvider.overrideWithValue(
          const Duration(milliseconds: 20),
        ),
      ],
    );
    fake.log = [_logLine('phase'), _logLine('rollout.started', label: 'base1')];
    final log = container.listen(harnessLogProvider(_runID), (_, _) {});
    await _pumpEvents();
    expect(log.read().value!.length, 2);

    fake.log = [...fake.log, _logLine('rollout.ended', label: 'base1')];
    await _pumpEvents();
    expect(log.read().value!.map((e) => e.type), [
      'phase',
      'rollout.started',
      'rollout.ended',
    ]);
    // Follow-up reads asked only for what was new.
    expect(fake.logReads.skip(1), everyElement(greaterThanOrEqualTo(2)));
  });

  test('small formatters', () {
    expect(harnessToolInput({'filePath': 'a/b.go', 'command': ''}), 'a/b.go');
    expect(harnessToolInput('not a map'), '');
    expect(harnessDuration(const Duration(seconds: 42)), '42s');
    expect(harnessDuration(const Duration(minutes: 3, seconds: 5)), '3m 05s');
    expect(harnessDuration(const Duration(hours: 1, minutes: 12)), '1h 12m');
  });

  test('the harness notice names what a harness brings', () {
    final data = <String, dynamic>{
      'loaded': true,
      'dir': '/p/.gocode/harness',
      'alwaysOn': ['systemprompt.md'],
      'skills': ['lint', 'docs'],
      'hooks': 1,
      'fingerprint': 'a3bd62d2f70f',
    };
    expect(harnessFacts(data), [
      '.gocode/harness',
      'systemprompt.md',
      '2 skills',
      '1 hook rule',
      'a3bd62d2f70f',
    ]);

    NoticeItem marker(Map<String, dynamic> d, String id) => NoticeItem(
      messageID: id,
      kind: Message.harness,
      text: '',
      timeCreated: 0,
      data: d,
    );
    expect(activeHarness([marker(data, 'm1')]), same(data));
    // The newest marker wins: removed means none is active.
    expect(
      activeHarness([
        marker(data, 'm1'),
        marker({'loaded': false}, 'm2'),
      ]),
      isNull,
    );
  });
}

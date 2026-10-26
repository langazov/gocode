import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/harness/harness_state.dart';

/// A stand-in gocode server for the harness surface: the runs list, budgets,
/// and an SSE stream the test writes `harness.*` events into.
class _FakeServer {
  _FakeServer(this._server);

  final HttpServer _server;
  final _sse = <HttpResponse>[];
  List<Map<String, Object?>> runs = [];

  int get port => _server.port;
  int runListReads = 0;

  /// Closes the current SSE connection(s), so the client reconnects — the
  /// network hiccup the lossy stream needs a reconcile for.
  void dropEventConnections() {
    for (final response in _sse.toList()) {
      unawaited(response.close());
    }
    _sse.clear();
  }

  static Future<_FakeServer> start() async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final fake = _FakeServer(server);
    server.listen(fake._handle);
    return fake;
  }

  /// Pushes a live harness event carrying the run's whole state — the shape
  /// server.HarnessPublisher sends.
  void emitHarnessEvent(Map<String, Object?> run, {String type = 'phase'}) {
    final event = jsonEncode({
      'id': 'e${DateTime.now().microsecondsSinceEpoch}',
      'type': 'harness.$type',
      'data': {'run': run, 'event': {'type': type}},
    });
    final frame = utf8.encode('data: $event\n\n');
    for (final response in _sse.toList()) {
      response.add(frame);
      unawaited(response.flush());
    }
  }

  Future<void> close() => _server.close(force: true);

  void _handle(HttpRequest request) {
    final path = request.uri.path;
    switch (path) {
      case '/api/health':
        request.response
          ..headers.contentType = ContentType.json
          ..write(jsonEncode({'healthy': true}));
        unawaited(request.response.close());
      case '/api/event':
        _sse.add(request.response);
        request.response
          ..headers.contentType = ContentType('text', 'event-stream')
          ..bufferOutput = false
          ..write(': ok\n\n');
        unawaited(request.response.flush());
      case '/api/harness/run':
        runListReads++;
        request.response
          ..headers.contentType = ContentType.json
          ..write(jsonEncode(runs));
        unawaited(request.response.close());
      case '/api/harness/budgets':
        request.response
          ..headers.contentType = ContentType.json
          ..write(jsonEncode([
            {
              'mode': 'parallel',
              'k': 1,
              'rollouts': 4,
              'edits': 3,
              'description': 'one Propose–Probe–Compose step',
            },
          ]));
        unawaited(request.response.close());
      default:
        request.response.statusCode = 404;
        unawaited(request.response.close());
    }
  }
}

Map<String, Object?> _run(String id, String status, {String? phase}) => {
  'id': id,
  'task': id.split('--').first,
  'domain': 'auth',
  'k': 1,
  'mode': 'parallel',
  'status': status,
  'phase': ?phase,
  'rows': [
    {'label': 'base1', 'phase': 'base', 'state': 'ok', 'costUSD': 0.4},
    {'label': 'inv1', 'phase': 'probe', 'state': 'pending', 'costUSD': 0},
  ],
  'costUSD': 0.4,
  'startedAt': '2025-06-01T10:00:00Z',
  'budget': 'parallel k=1 → 4 rollouts, 3 editor sessions',
};

Future<void> _pumpEvents() async {
  // Let the SSE read, the notifier's microtasks and timers run.
  for (var i = 0; i < 20; i++) {
    await Future<void>.delayed(const Duration(milliseconds: 5));
  }
}

void main() {
  test(
    'the notifier folds harness events, announces a run end once, and '
    'reconciles on reconnect',
    () async {
      final fake = await _FakeServer.start();
      final container = ProviderContainer();
      addTearDown(() async {
        container.dispose();
        await fake.close();
      });

      final runs = container.listen(
        harnessRunsProvider.select((s) => (loading: s.loading, runs: s.runs)),
        (_, _) {},
      );
      final ended = container.listen(
        harnessRunsProvider.select((s) => s.lastEnded),
        (_, _) {},
      );

      await container
          .read(connectionProvider.notifier)
          .apply(
            ConnectionSettings(
              mode: ConnectionMode.remote,
              remoteUrl: 'http://127.0.0.1:${fake.port}',
            ),
          );
      expect(container.read(connectionProvider).isConnected, isTrue);
      await _pumpEvents();

      // The initial load came from the server's list.
      expect(runs.read().loading, isFalse);
      expect(runs.read().runs, isEmpty);
      final readsAfterConnect = fake.runListReads;
      expect(readsAfterConnect, greaterThanOrEqualTo(1));

      // A live event inserts a new running run.
      fake.emitHarnessEvent(_run('fix-login--k1-parallel', 'running'));
      await _pumpEvents();
      expect(runs.read().runs.single.id, 'fix-login--k1-parallel');
      expect(runs.read().runs.single.isRunning, isTrue);

      // The same event again replaces by id without duplicating.
      fake.emitHarnessEvent(_run('fix-login--k1-parallel', 'running'));
      await _pumpEvents();
      expect(runs.read().runs.length, 1);

      // A run leaving `running` is announced exactly once.
      fake.emitHarnessEvent(
        _run('fix-login--k1-parallel', 'ok'),
        type: 'run.ended',
      );
      await _pumpEvents();
      final notice = ended.read();
      expect(notice, isNotNull);
      expect(notice!.run.status, 'ok');
      expect(notice.run.id, 'fix-login--k1-parallel');
      final announced = notice.noticeID;

      // The same end folded again — from a duplicate event, say — does not
      // announce twice.
      fake.emitHarnessEvent(_run('fix-login--k1-parallel', 'ok'));
      await _pumpEvents();
      expect(ended.read()!.noticeID, announced);

      // A dropped SSE connection reconciles from the server's ground truth.
      fake.runs = [_run('fix-login--k1-parallel', 'ok')];
      final beforeDrop = fake.runListReads;
      fake.dropEventConnections();
      // The SseClient's backoff reconnects; give it time.
      final deadline = DateTime.now().add(const Duration(seconds: 8));
      while (fake.runListReads <= beforeDrop &&
          DateTime.now().isBefore(deadline)) {
        await Future<void>.delayed(const Duration(milliseconds: 50));
      }
      await _pumpEvents();
      expect(fake.runListReads, greaterThan(beforeDrop));
      // The reconcile folded the server's finished run, not a new notice.
      expect(runs.read().runs.single.status, 'ok');
      expect(ended.read()!.noticeID, announced);
    },
    timeout: const Timeout(Duration(seconds: 30)),
  );

  test('polling runs only while a run is going', () async {
    final fake = await _FakeServer.start();
    fake.runs = [_run('sweep--k1-parallel', 'running')];
    final container = ProviderContainer(
      overrides: [
        // A fast clock so the 10 s backstop is testable.
        harnessPollIntervalProvider.overrideWithValue(
          const Duration(milliseconds: 60),
        ),
      ],
    );
    addTearDown(() async {
      container.dispose();
      await fake.close();
    });

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
    final baseline = fake.runListReads;

    // While the run is going, the poll keeps re-reading the list.
    await Future<void>.delayed(const Duration(milliseconds: 300));
    expect(fake.runListReads, greaterThan(baseline));

    // The run finishes: the poll stops.
    fake.runs = [_run('sweep--k1-parallel', 'ok')];
    await Future<void>.delayed(const Duration(milliseconds: 300));
    final settled = fake.runListReads;
    await Future<void>.delayed(const Duration(milliseconds: 300));
    // One last reconcile may land from the end event; after settling, no
    // more polls arrive.
    expect(
      fake.runListReads - settled,
      lessThan(3),
      reason: 'the poll should have stopped after the run finished',
    );
  }, timeout: const Timeout(Duration(seconds: 30)));

  test('ACP mode (no HTTP client) yields the unavailable state', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    final state = container.read(harnessRunsProvider);
    expect(state.unavailable, isTrue);
    expect(state.runs, isEmpty);
    expect(container.read(harnessAvailableProvider), isFalse);
  });
}

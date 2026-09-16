import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/core/connection/session_activity.dart';

/// A stand-in gocode server whose event stream can drop the
/// `session.next.run.ended` event (simulating gocode's lossy-by-design SSE
/// buffering), while `/api/session/{id}/status` always reports ground truth.
class _FakeServer {
  _FakeServer(this._server);

  final HttpServer _server;
  bool busy = true;
  int eventConnections = 0;
  HttpResponse? _firstEventResponse;

  int get port => _server.port;

  /// Simulates the server dropping the SSE connection (network hiccup),
  /// forcing the client to reconnect and fire `reconnectSignal`.
  Future<void> dropEventConnection() async {
    await _firstEventResponse?.close();
  }

  static Future<_FakeServer> start() async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final fake = _FakeServer(server);
    server.listen(fake._handle);
    return fake;
  }

  void _handle(HttpRequest request) {
    final path = request.uri.path;
    if (path == '/api/health') {
      request.response
        ..headers.contentType = ContentType.json
        ..write(jsonEncode({'healthy': true}));
      unawaited(request.response.close());
      return;
    }
    if (path == '/api/event') {
      eventConnections++;
      request.response
        ..headers.contentType = ContentType('text', 'event-stream')
        ..bufferOutput = false;
      // Only the *first* connection announces the run starting. The
      // matching run.ended is never sent on any connection — that's the
      // dropped event this test exercises.
      if (eventConnections == 1) {
        _firstEventResponse = request.response;
        final event = jsonEncode({
          'id': 'e1',
          'type': 'session.next.run.started',
          'sessionID': 's1',
        });
        request.response.write('data: $event\n\n');
      } else {
        request.response.write(': ok\n\n');
      }
      unawaited(request.response.flush());
      return;
    }
    if (path == '/api/session/s1/status') {
      request.response
        ..headers.contentType = ContentType.json
        ..write(jsonEncode({'busy': busy}));
      unawaited(request.response.close());
      return;
    }
    request.response.statusCode = 404;
    unawaited(request.response.close());
  }

  Future<void> close() => _server.close(force: true);
}

void main() {
  test('sessionActivityProvider clears a stuck busy flag on reconnect '
      'reconciliation after a dropped run.ended event', () async {
    final fake = await _FakeServer.start();
    final container = ProviderContainer();
    addTearDown(() async {
      container.dispose();
      await fake.close();
    });

    final activity = container.listen(sessionActivityProvider, (_, _) {});

    await container
        .read(connectionProvider.notifier)
        .apply(
          ConnectionSettings(
            mode: ConnectionMode.remote,
            remoteUrl: 'http://127.0.0.1:${fake.port}',
          ),
        );
    expect(container.read(connectionProvider).isConnected, isTrue);

    // The run.started event arrived; the sidebar now believes s1 is busy.
    await pumpEventQueue();
    expect(activity.read()['s1']?.busy, isTrue);

    // Simulate the server closing the SSE stream without ever sending
    // run.ended (the "lossy by design" drop) and the client reconnecting.
    // Ground truth is now idle.
    fake.busy = false;
    await fake.dropEventConnection();

    // Wait for the reconnect (backoff + reconnect) to happen and for
    // reconciliation to clear the stale busy flag.
    final deadline = DateTime.now().add(const Duration(seconds: 5));
    while (activity.read()['s1']?.busy != false &&
        DateTime.now().isBefore(deadline)) {
      await Future<void>.delayed(const Duration(milliseconds: 50));
    }

    expect(
      activity.read()['s1']?.busy,
      isFalse,
      reason:
          'stale busy flag should be corrected once the server is '
          'consulted directly, even though run.ended never arrived',
    );
  });
}

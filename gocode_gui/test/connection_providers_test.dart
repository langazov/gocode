import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/connection/controller.dart';

/// A stand-in gocode server: healthy, with an idle event stream.
Future<HttpServer> _fakeServer() async {
  final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
  server.listen((request) {
    switch (request.uri.path) {
      case '/api/health':
        request.response
          ..headers.contentType = ContentType.json
          ..write(jsonEncode({'healthy': true}));
        unawaited(request.response.close());
      case '/api/event':
        // Headers plus a keep-alive comment; the stream stays open.
        request.response
          ..headers.contentType = ContentType('text', 'event-stream')
          ..bufferOutput = false
          ..write(': ok\n\n');
        unawaited(request.response.flush());
      default:
        request.response.statusCode = 404;
        unawaited(request.response.close());
    }
  });
  return server;
}

void main() {
  test('apiClientProvider follows the connection after an early read',
      () async {
    final server = await _fakeServer();
    final container = ProviderContainer();
    addTearDown(() async {
      container.dispose();
      await server.close(force: true);
    });

    // Read before any connection exists — what AsksOverlay does at launch.
    // This used to pin null forever, leaving models/agents/sessions empty.
    final client = container.listen(apiClientProvider, (_, _) {});
    final controller = container.listen(connectionControllerProvider, (_, _) {});
    expect(client.read(), isNull);

    await container.read(connectionProvider.notifier).apply(
          ConnectionSettings(
            mode: ConnectionMode.remote,
            remoteUrl: 'http://127.0.0.1:${server.port}',
          ),
        );

    expect(container.read(connectionProvider).error, isNull);
    expect(container.read(connectionProvider).isConnected, isTrue);
    expect(client.read(), isNotNull);
    expect(client.read()!.baseUrl, 'http://127.0.0.1:${server.port}');
    expect(controller.read(), isNotNull);

    await container.read(connectionProvider.notifier).disconnect();
    expect(client.read(), isNull);
    expect(controller.read(), isNull);
  });
}

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/account/providers.dart';

/// A stand-in gocode server whose sign-in can change behind the app's back,
/// as a `gocode login` in another terminal does.
class _FakeServer {
  late final HttpServer _http;
  HttpResponse? _events;
  bool signedIn = false;
  int accountFetches = 0;

  String get url => 'http://127.0.0.1:${_http.port}';

  Future<void> start() async {
    _http = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    _http.listen((request) {
      final response = request.response;
      switch (request.uri.path) {
        case '/api/health':
          response
            ..headers.contentType = ContentType.json
            ..write(jsonEncode({'healthy': true}));
          unawaited(response.close());
        case '/api/event':
          response
            ..headers.contentType = ContentType('text', 'event-stream')
            ..bufferOutput = false
            ..write(': ok\n\n');
          unawaited(response.flush());
          _events = response;
        case '/api/account':
          accountFetches++;
          response
            ..headers.contentType = ContentType.json
            ..write(
              jsonEncode({
                'signedIn': signedIn,
                'site': 'https://gocoder.org',
                if (signedIn) 'email': 'a@b.c',
              }),
            );
          unawaited(response.close());
        default:
          response.statusCode = 404;
          unawaited(response.close());
      }
    });
  }

  Future<void> publish(String type) async {
    _events!.write('data: ${jsonEncode({'id': 'e1', 'type': type})}\n\n');
    await _events!.flush();
  }

  Future<void> close() => _http.close(force: true);
}

Future<void> _until(bool Function() condition) async {
  final deadline = DateTime.now().add(const Duration(seconds: 5));
  while (!condition()) {
    if (DateTime.now().isAfter(deadline)) fail('condition not met in time');
    await Future<void>.delayed(const Duration(milliseconds: 10));
  }
}

void main() {
  test(
    'accountProvider refetches when the server announces a sign-in',
    () async {
      final server = _FakeServer();
      await server.start();
      final container = ProviderContainer();
      addTearDown(() async {
        container.dispose();
        await server.close();
      });

      await container
          .read(connectionProvider.notifier)
          .apply(
            ConnectionSettings(
              mode: ConnectionMode.remote,
              remoteUrl: server.url,
            ),
          );
      final account = container.listen(accountProvider, (_, _) {});
      expect((await container.read(accountProvider.future))!.signedIn, isFalse);

      // Unrelated events leave the account alone.
      await server.publish('session.next.run.started');
      // Signed in from the TUI: the server's watcher announces it.
      server.signedIn = true;
      await server.publish(accountUpdatedEvent);

      await _until(() => account.read().value?.signedIn ?? false);
      expect(account.read().value!.email, 'a@b.c');
      expect(server.accountFetches, 2);
    },
  );
}

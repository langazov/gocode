import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/api/client.dart';
import 'package:gocode_gui/core/api/sse.dart';

/// Integration test against a real `gocode serve` child process.
///
/// Skipped automatically when the binary isn't available, so `flutter test`
/// stays green on any machine; run explicitly with a gocode on PATH.
void main() {
  final binary = Platform.environment['GOCODE_BIN'] ?? 'gocode';
  late Directory project;
  late Process server;
  var started = false;
  late String baseUrl;
  late GocodeClient client;

  Future<String> startServer() async {
    server = await Process.start(
      binary,
      ['serve', '--hostname', '127.0.0.1', '--port', '0'],
      workingDirectory: project.path,
      environment: {...Platform.environment, 'GOCODE_CLIENT': 'flutter-test'},
    );
    final completer = Completer<String>();
    final stdoutLines = <String>[];
    server.stdout
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen((line) {
          stdoutLines.add(line);
          final match = RegExp(r'listening on (https?://[^\s]+)')
              .firstMatch(line);
          if (match != null && !completer.isCompleted) {
            completer.complete(match.group(1)!.trim());
          }
        });
    server.stderr
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen((line) => debugPrint('gocode stderr: $line'));

    final String url;
    try {
      url = await completer.future.timeout(const Duration(seconds: 60));
    } on TimeoutException {
      throw StateError(
        'no listening line; stdout was:\n${stdoutLines.join('\n')}',
      );
    }

    // Health check with retry, mirroring the supervisor. Note the startup
    // line may report port 0 — resolve the real one via /api/health probes
    // only when the URL carries a real port.
    final probe = GocodeClient(baseUrl: url);
    for (var i = 0; i < 40; i++) {
      try {
        if (await probe.health()) {
          probe.close();
          return url;
        }
      } catch (_) {}
      await Future<void>.delayed(const Duration(milliseconds: 250));
    }
    probe.close();
    throw StateError('server never became healthy at $url');
  }

  setUpAll(() async {
    // Works with either a bare name (resolved via which) or an absolute
    // path from GOCODE_BIN. Sandboxed test runners may block `which`, so
    // an absolute path is preferred.
    final env = Platform.environment['GOCODE_BIN'];
    var found = false;
    if (env != null && env.isNotEmpty && File(env).existsSync()) {
      found = true;
    } else if (env == null || env.isEmpty || env == 'gocode') {
      final available = await Process.run('which', ['gocode']);
      found = available.exitCode == 0;
    }
    if (!found) {
      throw StateError('gocode not found; set GOCODE_BIN or add it to PATH');
    }
    project = await Directory.systemTemp.createTemp('gocode_gui_it_');
    client = GocodeClient(baseUrl: 'http://127.0.0.1:1'); // replaced below
    baseUrl = await startServer();
    started = true;
    client = GocodeClient(baseUrl: baseUrl);
  });

  tearDownAll(() async {
    if (started) {
      server.kill();
      await server.exitCode;
      await project.delete(recursive: true);
    }
  });

  test('health responds', () async {
    expect(await client.health(), isTrue);
  });

  test('creates a session and lists it', () async {
    final session = await client.createSession(
      directory: project.path,
      title: 'integration',
    );
    expect(session.directory, project.path);
    final sessions = await client.sessions();
    expect(sessions.map((s) => s.id), contains(session.id));
  });

  test('agents and commands are served', () async {
    final agents = await client.agents();
    expect(agents, isNotEmpty);
    final commands = await client.commands();
    expect(commands.map((c) => c.name), contains('init'));
  });

  test('empty message list for a fresh session', () async {
    final session = await client.createSession(directory: project.path);
    final messages = await client.messages(session.id);
    expect(messages, isEmpty);
    expect(await client.busy(session.id), isFalse);
  });

  test('vcs endpoints degrade outside a repository', () async {
    final vcs = await client.vcs();
    expect(vcs.inRepo, isFalse); // temp dir, not a git repo
    final diff = await client.vcsDiff();
    expect(diff, isEmpty);
  });

  test('SSE stream connects and stays open', () async {
    final sse = SseClient(baseUrl: baseUrl);
    var connected = false;
    final sub = sse.state.listen((s) {
      if (s == SseConnectionState.connected) connected = true;
    });
    await sse.start();
    await Future<void>.delayed(const Duration(milliseconds: 500));
    expect(connected, isTrue);
    await sub.cancel();
    sse.close();
  }, timeout: const Timeout(Duration(minutes: 2)));
}

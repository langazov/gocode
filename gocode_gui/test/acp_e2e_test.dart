// E2E smoke: drives the real `gocode acp` binary over stdio through the
// GUI's AcpClient — initialize, session lifecycle, list, delete. Skipped
// when no binary is found or GOCODE_GUI_ACP_E2E is unset, so CI (no
// binary) still passes.
//
// Run with:
//   GOCODE_GUI_ACP_E2E=1 GOCODE_BIN=/path/to/gocode flutter test test/acp_e2e_test.dart
import 'dart:io';

// ignore_for_file: avoid_print — diagnostics in an opt-in E2E test.
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/acp/client.dart';

void main() {
  final enabled = Platform.environment['GOCODE_GUI_ACP_E2E'] == '1';
    final bin = Platform.environment['GOCODE_BIN'] ?? 'gocode';

  test('initialize + session/new + list + delete against the real agent',
      () async {
    bool available = true;
    try {
      final result = await Process.run(bin, ['--version']);
      available = result.exitCode == 0;
    } catch (_) {
      available = false;
    }
    print('binary $bin available: $available');
    if (!enabled || !available) {
      print('skipping (GOCODE_GUI_ACP_E2E=$enabled)');
      return;
    }

    final cwd = Directory.systemTemp.createTempSync('gocode-acp-e2e');
    addTearDown(() {
      try {
        cwd.deleteSync(recursive: true);
      } catch (_) {}
    });

    final client = AcpClient(onLog: print);
    final info = await client.start(
      binaryPath: bin,
      workingDirectory: cwd.path,
    );
    print('agent: ${info.name} ${info.version} v${info.protocolVersion}');
    expect(info.protocolVersion, 1);
    expect(info.capabilities['loadSession'], isTrue);

    final setup = await client.newSession(cwd.path);
    print('session: ${setup.sessionID}');
    expect(setup.sessionID, isNotEmpty);
    expect(setup.configOptions.map((o) => o.id), contains('mode'));
    expect(setup.modes, isNotEmpty);

    final listed = await client.listSessions(cwd: cwd.path);
    print('listed ${listed.length} session(s)');
    expect(listed.any((s) => s.id == setup.sessionID), isTrue);

    await client.deleteSession(setup.sessionID);
    final after = await client.listSessions(cwd: cwd.path);
    expect(after.any((s) => s.id == setup.sessionID), isFalse);

    await client.stop();
  }, timeout: const Timeout(Duration(minutes: 2)));
}

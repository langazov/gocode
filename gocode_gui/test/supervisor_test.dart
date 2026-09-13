import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/process/supervisor.dart';

void main() {
  group('ServerSupervisor.listeningRe', () {
    test('matches the gocode startup line', () {
      const line = 'gocode server listening on http://127.0.0.1:54321';
      final match = ServerSupervisor.listeningRe.firstMatch(line);
      expect(match, isNotNull);
      expect(match!.group(1), 'http://127.0.0.1:54321');
    });

    test('matches an IPv6 loopback', () {
      const line = 'gocode server listening on http://[::1]:8080';
      final match = ServerSupervisor.listeningRe.firstMatch(line);
      expect(match!.group(1), 'http://[::1]:8080');
    });

    test('does not match unrelated lines', () {
      expect(
        ServerSupervisor.listeningRe.firstMatch('Warning: password unset'),
        isNull,
      );
    });

    test('extracts from a line with surrounding text', () {
      const line = 'boot: pid 1 … gocode server listening on http://127.0.0.1:1 ok';
      expect(
        ServerSupervisor.listeningRe.firstMatch(line)!.group(1),
        'http://127.0.0.1:1',
      );
    });
  });
}

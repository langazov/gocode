import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/acp/json_rpc.dart';

void main() {
  group('JsonRpcConnection', () {
    test('correlates requests by id', () async {
      final incoming = StreamController<String>();
      final sent = <String>[];
      final conn = JsonRpcConnection(
        incoming: incoming.stream,
        send: sent.add,
      );

      final first = conn.request('a');
      final second = conn.request('b');
      // Replies may arrive out of order.
      await Future<void>.delayed(Duration.zero);
      final ids = [
        for (final line in sent) jsonDecode(line)['id'] as int,
      ];
      incoming.add(jsonEncode({
        'jsonrpc': '2.0',
        'id': ids[1],
        'result': {'value': 'b'},
      }));
      incoming.add(jsonEncode({
        'jsonrpc': '2.0',
        'id': ids[0],
        'result': {'value': 'a'},
      }));
      expect((await first)['value'], 'a');
      expect((await second)['value'], 'b');
      conn.close();
    });

    test('rejects with the peer error', () async {
      final incoming = StreamController<String>();
      final sent = <String>[];
      final conn = JsonRpcConnection(
        incoming: incoming.stream,
        send: sent.add,
      );
      final pending = conn.request('initialize');
      await Future<void>.delayed(Duration.zero);
      final id = (jsonDecode(sent.single) as Map<String, dynamic>)['id'];
      incoming.add(jsonEncode({
        'jsonrpc': '2.0',
        'id': id,
        'error': {'code': -32002, 'message': 'session not found'},
      }));
      await expectLater(
        pending,
        throwsA(
          isA<JsonRpcException>()
              .having((e) => e.code, 'code', -32002)
              .having((e) => e.message, 'message', 'session not found'),
        ),
      );
      conn.close();
    });

    test('notifications stream to listeners', () async {
      final incoming = StreamController<String>();
      final conn = JsonRpcConnection(
        incoming: incoming.stream,
        send: (_) {},
      );
      final seen = <String>[];
      final sub = conn.notifications.listen((n) => seen.add(n.method));
      incoming.add(jsonEncode({
        'jsonrpc': '2.0',
        'method': 'session/update',
        'params': {'sessionId': 'ses_1', 'update': {}},
      }));
      await Future<void>.delayed(Duration.zero);
      expect(seen, ['session/update']);
      await sub.cancel();
      conn.close();
    });

    test('answers server requests through the registered handler',
        () async {
      final incoming = StreamController<String>();
      final sent = <String>[];
      final conn = JsonRpcConnection(
        incoming: incoming.stream,
        send: sent.add,
      );
      conn.handle('session/request_permission', (params) async {
        return {
          'outcome': {
            'outcome': 'selected',
            'optionId': params['options'][0]['optionId'],
          },
        };
      });
      incoming.add(jsonEncode({
        'jsonrpc': '2.0',
        'id': 7,
        'method': 'session/request_permission',
        'params': {
          'options': [
            {'optionId': 'once'},
          ],
        },
      }));
      await Future<void>.delayed(Duration.zero);
      final reply = jsonDecode(sent.single) as Map<String, dynamic>;
      expect(reply['id'], 7);
      expect(reply['result']['outcome']['optionId'], 'once');
      conn.close();
    });

    test('unhandled server requests are refused with MethodNotFound',
        () async {
      final incoming = StreamController<String>();
      final sent = <String>[];
      final conn = JsonRpcConnection(
        incoming: incoming.stream,
        send: sent.add,
      );
      incoming.add(jsonEncode({
        'jsonrpc': '2.0',
        'id': 3,
        'method': 'fs/read_text_file',
        'params': {},
      }));
      await Future<void>.delayed(Duration.zero);
      final reply = jsonDecode(sent.single) as Map<String, dynamic>;
      expect(reply['error']['code'], -32601);
      conn.close();
    });

    test('a closed transport fails pending requests', () async {
      final incoming = StreamController<String>();
      final conn = JsonRpcConnection(
        incoming: incoming.stream,
        send: (_) {},
      );
      final pending = conn.request('session/prompt');
      await Future<void>.delayed(Duration.zero);
      await incoming.close();
      await expectLater(pending, throwsStateError);
      conn.close();
    });
  });
}

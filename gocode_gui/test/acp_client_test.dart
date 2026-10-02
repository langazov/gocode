import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/acp/client.dart';
import 'package:gocode_gui/core/acp/projection.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/features/session/timeline.dart';

/// A scripted gocode-acp stand-in: a real line-delimited JSON-RPC peer that
/// answers initialize/session lifecycle and streams the updates the test
/// queues. Wire shapes mirror internal/acp's own tests.
class FakeAcpAgent {
  FakeAcpAgent(this.incoming);

  /// Lines arriving from the client.
  final Stream<String> incoming;
  final StreamController<String> _outgoing = StreamController<String>();

  final Map<String, Future<Object?> Function(Map<String, dynamic>)> _methods =
      <String, Future<Object?> Function(Map<String, dynamic>)>{};

  String? sessionID;
  List<Map<String, dynamic>> replayOnLoad = const [];
  final List<Map<String, dynamic>> permissionAsks = [];
  void Function(Map<String, dynamic> params, Completer<Map<String, dynamic>> reply)?
      onPermission;

  Stream<String> get stream => _outgoing.stream;

  void start() {
    incoming.listen(_onLine);
    _methods['initialize'] = (params) async {
      return {
        'protocolVersion': 1,
        'agentCapabilities': {
          'loadSession': true,
          'promptCapabilities': {'image': true},
          'sessionCapabilities': {
            'list': {},
            'resume': {},
            'close': {},
            'delete': {},
            'additionalDirectories': {},
          },
        },
        'agentInfo': {'name': 'gocode', 'title': 'gocode', 'version': 'test'},
        'authMethods': [],
      };
    };
    _methods['session/new'] = (params) async {
      sessionID = 'ses_new_1';
      return {
        'sessionId': sessionID,
        'configOptions': _configOptions(),
        'modes': {
          'currentModeId': 'build',
          'availableModes': [
            {'id': 'build', 'name': 'Build'},
            {'id': 'plan', 'name': 'Plan'},
          ],
        },
      };
    };
    _methods['session/load'] = (params) async {
      sessionID = params['sessionId'];
      for (final update in replayOnLoad) {
        _notify('session/update', {
          'sessionId': sessionID,
          'update': update,
        });
      }
      return {
        'sessionId': sessionID,
        'configOptions': _configOptions(),
        'modes': {
          'currentModeId': 'build',
          'availableModes': [
            {'id': 'build', 'name': 'Build'},
          ],
        },
      };
    };
    _methods['session/list'] = (params) async {
      return {
        'sessions': [
          {
            'sessionId': 'ses_1',
            'cwd': '/tmp/project',
            'title': 'A session',
            'updatedAt': '2026-01-01T00:00:00Z',
          },
        ],
      };
    };
    _methods['session/delete'] = (params) async => {};
    _methods['session/set_mode'] = (params) async => {};
    _methods['session/set_config_option'] = (params) async {
      return {'configOptions': _configOptions()};
    };
    _methods['session/prompt'] = (params) async {
      _promptCompleter = Completer<Map<String, dynamic>>();
      return _promptCompleter!.future;
    };
  }

  Completer<Map<String, dynamic>>? _promptCompleter;

  List<Map<String, dynamic>> _configOptions() => [
        {
          'id': 'mode',
          'name': 'Mode',
          'category': 'mode',
          'type': 'select',
          'currentValue': 'build',
          'options': [
            {'value': 'build', 'name': 'Build'},
            {'value': 'plan', 'name': 'Plan'},
          ],
        },
        {
          'id': 'model',
          'name': 'Model',
          'category': 'model',
          'type': 'select',
          'currentValue': 'faketest/model-a',
          'options': [
            {
              'group': 'faketest',
              'name': 'FakeTest',
              'options': [
                {'value': 'faketest/model-a', 'name': 'Model A'},
                {'value': 'faketest/model-b', 'name': 'Model B'},
              ],
            },
          ],
        },
      ];

  void _onLine(String line) {
    final decoded = jsonDecode(line);
    final entries = decoded is List ? decoded : [decoded];
    for (final entry in entries) {
      if (entry is Map<String, dynamic>) _onMessage(entry);
    }
  }

  void _onMessage(Map<String, dynamic> message) {
    final id = message['id'];
    final method = message['method'];
    if (id != null && method is String) {
      final handler = _methods[method];
      if (handler == null) {
        _reply(id, error: {'code': -32601, 'message': 'no $method'});
        return;
      }
      final params =
          message['params'] is Map<String, dynamic>
              ? message['params'] as Map<String, dynamic>
              : <String, dynamic>{};
      handler(params).then(
        (result) => _reply(id, result: result ?? {}),
        onError: (Object e) => _reply(id, error: {'code': -32000, 'message': '$e'}),
      );
      return;
    }
    if (method == 'session/cancel' && _promptCompleter != null) {
      _promptCompleter?.complete({'stopReason': 'cancelled'});
    }
  }

  void _reply(dynamic id, {Object? result, Map<String, dynamic>? error}) {
    _outgoing.add(
      jsonEncode({
        'jsonrpc': '2.0',
        'id': id,
        if (error != null) 'error': error else 'result': result ?? {},
      }),
    );
  }

  /// Streams one session/update to the client.
  void update(String sessionID, Map<String, dynamic> update) {
    _notify('session/update', {'sessionId': sessionID, 'update': update});
  }

  void _notify(String method, Map<String, dynamic> params) {
    _outgoing.add(
      jsonEncode({'jsonrpc': '2.0', 'method': method, 'params': params}),
    );
  }

  /// Sends a permission ask as a server→client request.
  void askPermission(String sessionID, String callID) {
    final id = 'perm_${permissionAsks.length + 1}';
    permissionAsks.add({'id': id});
    _outgoing.add(
      jsonEncode({
        'jsonrpc': '2.0',
        'id': id,
        'method': 'session/request_permission',
        'params': {
          'sessionId': sessionID,
          'options': [
            {'optionId': 'once', 'name': 'Allow once', 'kind': 'allow_once'},
            {
              'optionId': 'always',
              'name': 'Always allow',
              'kind': 'allow_always',
            },
            {'optionId': 'reject', 'name': 'Reject', 'kind': 'reject_once'},
          ],
          'toolCall': {
            'toolCallId': callID,
            'title': 'Allow running `echo hi`?',
            'status': 'pending',
          },
        },
      }),
    );
  }

  void close() {
    _outgoing.close();
  }
}

/// Wires a client to a fake agent over in-memory lines.
(AcpClient, FakeAcpAgent) makePair() {
  final toAgent = StreamController<String>();
  final agent = FakeAcpAgent(toAgent.stream);
  agent.start();
  final client = AcpClient();
  unawaited(
    client.connectForTest(incoming: agent.stream, send: toAgent.add),
  );
  addTearDown(() {
    agent.close();
    toAgent.close();
  });
  return (client, agent);
}

void main() {
  test('initialize negotiates v1 and reports agent info', () async {
    final (client, _) = await makePairReady();
    final info = agentInfoOf(client);
    expect(info.protocolVersion, 1);
    expect(info.name, 'gocode');
    expect(agentInfoOf(client).capabilities['loadSession'], isTrue);
  });

  test('session/new returns setup state', () async {
    final (client, _) = makePair();
    await makeReady(client);
    final setup = await client.newSession('/tmp/project');
    expect(setup.sessionID, 'ses_new_1');
    expect(
      setup.configOptions.map((o) => o.id),
      containsAll(['mode', 'model']),
    );
    final model = setup.configOptions.singleWhere((o) => o.id == 'model');
    // Groups are flattened into description-carrying values.
    expect(model.values.length, 2);
    expect(model.values.first.description, 'FakeTest');
    expect(setup.modes.map((m) => m.id), ['build', 'plan']);
  });

  test('session/list decodes entries', () async {
    final (client, _) = makePair();
    await makeReady(client);
    final sessions = await client.listSessions();
    expect(sessions, hasLength(1));
    expect(sessions.first.id, 'ses_1');
    expect(sessions.first.cwd, '/tmp/project');
    expect(sessions.first.title, 'A session');
  });

  test('prompt streams updates and resolves with the stop reason', () async {
    final (client, agent) = makePair();
    await makeReady(client);
    final setup = await client.newSession('/tmp/project');

    final updates = <String>[];
    final sub = client.updates.listen((u) => updates.add(u.kind));
    final prompted = client.prompt(setup.sessionID, 'run it');

    await Future<void>.delayed(Duration.zero);
    agent.update(setup.sessionID, {
      'sessionUpdate': 'user_message_chunk',
      'messageId': 'msg_1',
      'content': {'type': 'text', 'text': 'run it'},
    });
    agent.update(setup.sessionID, {
      'sessionUpdate': 'agent_message_chunk',
      'messageId': 'msg_2-text',
      'content': {'type': 'text', 'text': 'Hello '},
    });
    agent.update(setup.sessionID, {
      'sessionUpdate': 'agent_message_chunk',
      'messageId': 'msg_2-text',
      'content': {'type': 'text', 'text': 'there'},
    });
    agent.update(setup.sessionID, {
      'sessionUpdate': 'tool_call',
      'toolCallId': 'msg_2-call_1',
      'name': 'bash',
      'title': 'echo hi',
      'kind': 'execute',
      'status': 'pending',
      'rawInput': {'command': 'echo hi'},
    });
    agent.update(setup.sessionID, {
      'sessionUpdate': 'tool_call_update',
      'toolCallId': 'msg_2-call_1',
      'status': 'completed',
      'rawOutput': {'output': 'hi\n'},
    });
    agent.endPrompt('end_turn');

    final result = await prompted;
    expect(result.stopReason, 'end_turn');
    expect(
      updates,
      containsAll([
        'user_message_chunk',
        'agent_message_chunk',
        'tool_call',
        'tool_call_update',
      ]),
    );
    await sub.cancel();
  });

  test('permission asks surface and can be answered', () async {
    final (client, agent) = makePair();
    await makeReady(client);
    final setup = await client.newSession('/tmp/project');

    final asks = <AcpPermissionRequest>[];
    final sub = client.permissionRequests.listen(asks.add);

    agent.askPermission(setup.sessionID, 'msg_2-call_1');
    await Future<void>.delayed(Duration.zero);
    expect(asks, hasLength(1));
    expect(asks.first.title, contains('echo hi'));
    expect(asks.first.options.map((o) => o.id), ['once', 'always', 'reject']);

    await client.replyPermission(asks.first, 'once');
    await Future<void>.delayed(Duration.zero);
    await sub.cancel();
  });

  test('projection groups parts by turn and settles tools', () {
    final session = Session(
      id: 'ses_1',
      title: 't',
      directory: '/tmp',
      timeCreated: 0,
      timeUpdated: 0,
    );
    final projection = AcpProjection(session);

    bool changedFor(Map<String, dynamic> raw) => projection.apply(
          AcpSessionUpdate({...raw, 'sessionId': 'ses_1'}),
        );

    expect(
      changedFor({
        'sessionUpdate': 'user_message_chunk',
        'messageId': 'msg_1',
        'content': {'type': 'text', 'text': 'run it'},
      }),
      isTrue,
    );
    expect(
      changedFor({
        'sessionUpdate': 'agent_message_chunk',
        'messageId': 'msg_2-text',
        'content': {'type': 'text', 'text': 'Hi'},
      }),
      isTrue,
    );
    expect(
      changedFor({
        'sessionUpdate': 'tool_call',
        'toolCallId': 'msg_2-call_1',
        'name': 'bash',
        'rawInput': {'command': 'echo hi'},
      }),
      isTrue,
    );
    expect(
      changedFor({
        'sessionUpdate': 'tool_call_update',
        'toolCallId': 'msg_2-call_1',
        'status': 'in_progress',
      }),
      isTrue,
    );

    final items = projection.items;
    expect(items, hasLength(2));
    expect(items.first, isA<UserBubble>());
    final turn = items[1] as AssistantTurn;
    expect(turn.parts, hasLength(2));
    expect(turn.parts.first.isText, isTrue);
    expect(turn.textFor(turn.parts.first), 'Hi');
    final tool = turn.parts.last;
    expect(tool.state!.status, ToolState.running);
    expect(tool.state!.input, {'command': 'echo hi'});

    expect(
      changedFor({
        'sessionUpdate': 'tool_call_update',
        'toolCallId': 'msg_2-call_1',
        'status': 'completed',
        'content': [
          {
            'type': 'content',
            'content': {'type': 'text', 'text': 'hi\n'},
          },
        ],
      }),
      isTrue,
    );
    // The settle replaced the part object (its fields are final), so
    // re-read it from the turn.
    final settled = turn.parts.lastWhere((p) => p.id == 'msg_2-call_1');
    expect(settled.state!.isDone, isTrue);
    expect(settled.state!.output, contains('hi'));
  });

  test('projection applies the plan and session info', () {
    final session = Session(
      id: 'ses_1',
      title: 't',
      directory: '/tmp',
      timeCreated: 0,
      timeUpdated: 0,
    );
    final projection = AcpProjection(session);
    projection.apply(AcpSessionUpdate({
      'sessionUpdate': 'plan',
      'entries': [
        {'content': 'first', 'status': 'in_progress', 'priority': 'high'},
        {'content': 'second', 'status': 'pending', 'priority': 'low'},
      ],
    }));
    expect(projection.todos, hasLength(2));
    expect(projection.todos.first.isInProgress, isTrue);

    projection.apply(AcpSessionUpdate({
      'sessionUpdate': 'session_info_update',
      'title': 'Renamed by the agent',
    }));
    expect(projection.title, 'Renamed by the agent');
  });
}

AcpAgentInfo agentInfoOf(AcpClient client) => client.agentInfo!;

/// Waits for the pair's initialize round-trip.
Future<void> makeReady(AcpClient client) async {
  final deadline = DateTime.now().add(const Duration(seconds: 2));
  while (client.agentInfo == null && DateTime.now().isBefore(deadline)) {
    await Future<void>.delayed(const Duration(milliseconds: 5));
  }
  if (client.agentInfo == null) {
    throw StateError('initialize never completed');
  }
}

Future<(AcpClient, FakeAcpAgent)> makePairReady() async {
  final pair = makePair();
  await makeReady(pair.$1);
  return pair;
}

extension on FakeAcpAgent {
  /// Resolves the pending prompt request with a stop reason.
  void endPrompt(String stopReason) {
    _promptCompleter?.complete({'stopReason': stopReason});
  }
}

import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/features/session/timeline.dart';

void main() {
  group('projectMessages', () {
    test('user message becomes a bubble', () {
      final items = projectMessages([
        Message(
          id: 'm1',
          sessionID: 's1',
          type: Message.user,
          seq: 1,
          timeCreated: 1,
          data: {'text': 'hello', 'files': []},
        ),
      ]);
      expect(items, hasLength(1));
      expect(items.first, isA<UserBubble>());
      expect((items.first as UserBubble).text, 'hello');
    });

    test('assistant message projects parts in order', () {
      final items = projectMessages([
        Message(
          id: 'm2',
          sessionID: 's1',
          type: Message.assistant,
          seq: 2,
          timeCreated: 2,
          data: {
            'agent': 'build',
            'model': {'providerID': 'anthropic', 'id': 'claude'},
            'time': {'created': 1, 'completed': 2},
            'content': [
              {'type': 'reasoning', 'id': 'p1', 'text': 'hmm'},
              {'type': 'text', 'id': 'p2', 'text': 'answer'},
              {
                'type': 'tool',
                'id': 'p3',
                'name': 'bash',
                'state': {
                  'status': 'completed',
                  'input': {'command': 'ls'},
                  'output': 'a\nb',
                },
              },
            ],
            'tokens': {
              'input': 10,
              'output': 5,
              'reasoning': 0,
              'cache': {'read': 0, 'write': 0},
            },
            'cost': 0.01,
          },
        ),
      ]);
      final turn = items.single as AssistantTurn;
      expect(turn.agent, 'build');
      expect(turn.parts.map((p) => p.type), ['reasoning', 'text', 'tool']);
      expect(turn.textFor(turn.parts[1]), 'answer');
      expect(turn.parts[2].state!.isDone, isTrue);
      expect(turn.tokens!.total, 15);
      expect(turn.cost, 0.01);
    });

    test('notice items for agent/model switches and compaction', () {
      final items = projectMessages([
        Message(
          id: 'a',
          sessionID: 's',
          type: Message.agentSwitched,
          seq: 1,
          timeCreated: 1,
          data: {'agent': 'plan'},
        ),
        Message(
          id: 'b',
          sessionID: 's',
          type: Message.compaction,
          seq: 2,
          timeCreated: 2,
          data: {},
        ),
      ]);
      expect((items[0] as NoticeItem).text, 'agent → plan');
      expect((items[1] as NoticeItem).text, 'context compacted');
    });

    test('aborted error is distinguishable', () {
      final items = projectMessages([
        Message(
          id: 'm',
          sessionID: 's',
          type: Message.assistant,
          seq: 1,
          timeCreated: 1,
          data: {
            'agent': 'build',
            'model': {'providerID': 'x', 'id': 'y'},
            'time': {'created': 1},
            'content': [],
            'error': {'type': 'aborted'},
          },
        ),
      ]);
      expect((items.single as AssistantTurn).error!.isAborted, isTrue);
    });
  });

  group('AssistantTurn streaming', () {
    test('deltas append and reconcile replaces', () {
      final turn = AssistantTurn(
        messageID: 'm',
        agent: 'build',
        model: const ModelRef(providerID: 'p', id: 'x'),
        timeCreated: 0,
      );
      turn.applyDelta('p1', 'hel');
      turn.applyDelta('p1', 'lo');
      expect(turn.textFor(AssistantPart(type: 'text', id: 'p1')), 'hello');

      turn.reconcileParts([
        const AssistantPart(type: 'text', id: 'p1', text: 'settled'),
      ]);
      expect(turn.textFor(turn.parts.single), 'settled');
    });
  });

  group('PermissionRequest.detail', () {
    test('prefers metadata over resources', () {
      final r = PermissionRequest.fromJson({
        'id': '1',
        'sessionID': 's',
        'action': 'bash',
        'resources': ['rm -rf'],
        'metadata': {'command': 'ls -la'},
      });
      expect(r.detail, 'ls -la');
    });

    test('falls back to resources', () {
      final r = PermissionRequest.fromJson({
        'id': '1',
        'sessionID': 's',
        'action': 'edit',
        'resources': ['/a/b.go'],
      });
      expect(r.detail, '/a/b.go');
    });
  });

  group('ApiEvent.fromJson', () {
    test('reads the wire shape', () {
      final e = ApiEvent.fromJson({
        'id': 'e1',
        'type': 'session.next.text.delta',
        'sessionID': 'ses_1',
        'seq': 4,
        'data': {'messageID': 'm', 'delta': 'x'},
      });
      expect(e.sessionID, 'ses_1');
      expect(e.seq, 4);
      expect(e.data['delta'], 'x');
    });
  });
}

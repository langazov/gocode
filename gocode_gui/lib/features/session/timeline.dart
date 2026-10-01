import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/acp/connection.dart';
import '../../core/api/client.dart';
import '../../core/api/models.dart' hide Provider;
import '../../core/connection/controller.dart';

/// One renderable row in a session timeline.
sealed class TimelineItem {
  const TimelineItem();

  String get key;
}

class UserBubble extends TimelineItem {
  const UserBubble({
    required this.messageID,
    required this.text,
    required this.files,
    required this.timeCreated,
  });

  final String messageID;
  final String text;
  final List<FileAttachment> files;
  final int timeCreated;

  @override
  String get key => messageID;
}

/// An assistant turn: streaming text, reasoning blocks, tool calls.
class AssistantTurn extends TimelineItem {
  AssistantTurn({
    required this.messageID,
    required this.agent,
    required this.model,
    required this.timeCreated,
  });

  final String messageID;
  String agent;
  ModelRef model;
  final int timeCreated;

  /// Parts in insertion order.
  final List<AssistantPart> parts = [];

  /// Accumulated text per part id (streaming deltas append here).
  final Map<String, StringBuffer> _textBuffers = {};

  String? finish;
  AssistantTokens? tokens;
  double? cost;
  AssistantError? error;

  String textFor(AssistantPart part) =>
      _textBuffers[part.id]?.toString() ?? part.text ?? '';

  /// Replaces parts wholesale (reconcile from GET /message).
  void reconcileParts(List<AssistantPart> incoming) {
    parts
      ..clear()
      ..addAll(incoming);
    for (final p in incoming) {
      _textBuffers[p.id] = StringBuffer(p.text ?? '');
    }
  }

  /// Applies a streaming delta to a text/reasoning part.
  void applyDelta(String partID, String delta) {
    _textBuffers.putIfAbsent(partID, StringBuffer.new).write(delta);
  }

  AssistantPart? partByID(String id) {
    for (final p in parts) {
      if (p.id == id) return p;
    }
    return null;
  }

  @override
  String get key => messageID;
}

/// A synthetic/system notice (agent switched, model switched, compaction).
class NoticeItem extends TimelineItem {
  const NoticeItem({
    required this.messageID,
    required this.kind,
    required this.text,
    required this.timeCreated,
  });

  final String messageID;
  final String kind;
  final String text;
  final int timeCreated;

  @override
  String get key => messageID;
}

/// Everything the session screen renders.
class SessionState {
  const SessionState({
    required this.session,
    required this.items,
    this.busy = false,
    this.queued = const [],
    this.todos = const [],
    this.stats,
    this.acpConfigOptions,
  });

  final Session session;
  final List<TimelineItem> items;
  final bool busy;
  final List<QueuedPrompt> queued;
  final List<Todo> todos;
  final SessionStats? stats;

  /// The ACP session's config options, when this state came from the ACP
  /// transport; null over HTTP. Pickers read the model/thought_level
  /// options from here instead of the model catalog.
  final List<Object>? acpConfigOptions;

  SessionState copyWith({
    Session? session,
    List<TimelineItem>? items,
    bool? busy,
    List<QueuedPrompt>? queued,
    List<Todo>? todos,
    SessionStats? stats,
    List<Object>? acpConfigOptions,
  }) => SessionState(
    session: session ?? this.session,
    items: items ?? this.items,
    busy: busy ?? this.busy,
    queued: queued ?? this.queued,
    todos: todos ?? this.todos,
    stats: stats ?? this.stats,
    acpConfigOptions: acpConfigOptions ?? this.acpConfigOptions,
  );
}

/// Projects durable messages into timeline items. Pure; unit-testable.
List<TimelineItem> projectMessages(List<Message> messages) {
  final out = <TimelineItem>[];
  for (final m in messages) {
    switch (m.type) {
      case Message.user:
        final data = UserData.fromJson(m.data);
        out.add(
          UserBubble(
            messageID: m.id,
            text: data.text,
            files: data.files,
            timeCreated: m.timeCreated,
          ),
        );
      case Message.assistant:
        final data = AssistantData.fromJson(m.data);
        final turn = AssistantTurn(
          messageID: m.id,
          agent: data.agent,
          model: data.model,
          timeCreated: m.timeCreated,
        );
        turn.reconcileParts(data.parts);
        turn
          ..finish = data.finish
          ..tokens = data.tokens
          ..cost = data.cost
          ..error = data.error;
        out.add(turn);
      default:
        out.add(_noticeFor(m));
    }
  }
  return out;
}

NoticeItem _noticeFor(Message m) {
  var text = m.type;
  if (m.type == Message.agentSwitched) {
    text = 'agent → ${m.data['agent'] ?? '?'}';
  } else if (m.type == Message.modelSwitched) {
    final model = m.data['model'];
    text = 'model → ${model is Map ? model['id'] : '?'}';
  } else if (m.type == Message.compaction) {
    text = 'context compacted';
  }
  return NoticeItem(
    messageID: m.id,
    kind: m.type,
    text: text,
    timeCreated: m.timeCreated,
  );
}

/// The imperative surface a session screen drives, whichever transport
/// backs it: HTTP ([SessionController]) or ACP ([AcpSessionController]).
abstract class SessionBackend {
  String get sessionID;
  Stream<SessionState> get stream;

  Future<void> prompt(
    String text, {
    String delivery,
    List<FileAttachment> files,
  });

  Future<void> interrupt();
  Future<void> setAgent(String agent);
  Future<void> setModel(
    String providerID,
    String modelID, {
    String? variant,
  });
}

/// Drives one session's [SessionState] stream: initial reconcile, live SSE
/// events, and reconcile-on-reconnect (gocode subscriptions are lossy by
/// design, so every reconnect re-fetches durable state).
class SessionController implements SessionBackend {
  SessionController({
    required GocodeClient client,
    required this.sessionID,
    required Stream<ApiEvent> events,
    Stream<void>? reconnectSignal,
  }) : _client = client,
       _events = events,
       _reconnectSignal = reconnectSignal;

  final GocodeClient _client;
  @override
  final String sessionID;
  final Stream<ApiEvent> _events;
  final Stream<void>? _reconnectSignal;

  final _stream = StreamController<SessionState>.broadcast();
  @override
  Stream<SessionState> get stream => _stream.stream;

  StreamSubscription<ApiEvent>? _eventSub;
  StreamSubscription<void>? _reconnectSub;
  SessionState? _state;
  bool _closed = false;

  Future<void> start() async {
    _eventSub = _events
        .where((e) => e.sessionID == null || e.sessionID == sessionID)
        .listen(_onEvent, onError: (Object _) {});
    _reconnectSub = _reconnectSignal?.listen((_) => reconcile());
    await reconcile();
  }

  /// Re-fetches all durable state and emits a fresh projection.
  Future<void> reconcile() async {
    if (_closed) return;
    try {
      final results = await Future.wait<dynamic>([
        _client.session(sessionID),
        _client.messages(sessionID),
        _client.busy(sessionID),
        _client.queue(sessionID),
        _client.todos(sessionID),
        _client.stats(sessionID),
      ]);
      if (_closed) return;
      _state = SessionState(
        session: results[0] as Session,
        items: projectMessages(results[1] as List<Message>),
        busy: results[2] as bool,
        queued: results[3] as List<QueuedPrompt>,
        todos: results[4] as List<Todo>,
        stats: results[5] as SessionStats,
      );
      _stream.add(_state!);
    } catch (e) {
      _stream.addError(e);
    }
  }

  void _onEvent(ApiEvent event) {
    final current = _state;
    if (current == null) return;
    switch (event.type) {
      case 'session.next.run.started':
        _state = current.copyWith(busy: true);
        _stream.add(_state!);
      case 'session.next.run.ended':
        // The settled turn changed durable state; reconcile after the flag.
        _state = current.copyWith(busy: false);
        _stream.add(_state!);
        unawaited(reconcile());
      case 'session.next.text.delta':
        _applyDelta(event, AssistantPart.textType);
      case 'session.next.reasoning.delta':
        _applyDelta(event, AssistantPart.reasoningType);
      default:
        // Tool settlement, step boundaries, todos, permissions… anything
        // else that committed may have changed the projection.
        unawaited(reconcile());
    }
  }

  void _applyDelta(ApiEvent e, String kind) {
    final current = _state;
    if (current == null) return;
    final data = e.data;
    final messageID = data['messageID'] as String?;
    final partID = data['partID'] as String? ?? data['id'] as String?;
    final delta = data['delta'] as String? ?? data['text'] as String? ?? '';
    if (messageID == null || partID == null) return;

    AssistantTurn? turn;
    for (final item in current.items.reversed) {
      if (item is AssistantTurn && item.messageID == messageID) {
        turn = item;
        break;
      }
    }
    // A delta arriving for a message the reconcile hasn't seen yet: create a
    // stub turn so the stream shows live text immediately.
    turn ??= _appendStubTurn(current, messageID);

    var part = turn.partByID(partID);
    if (part == null) {
      part = AssistantPart(type: kind, id: partID);
      turn.parts.add(part);
    }
    turn.applyDelta(partID, delta);
    _stream.add(current.copyWith(items: List.of(current.items)));
  }

  AssistantTurn _appendStubTurn(SessionState current, String messageID) {
    final turn = AssistantTurn(
      messageID: messageID,
      agent: current.session.agent ?? '',
      model: current.session.model ?? const ModelRef(providerID: '', id: ''),
      timeCreated: DateTime.now().millisecondsSinceEpoch,
    );
    current.items.add(turn);
    return turn;
  }

  /// Sends a prompt; the server admits it durably and events drive the rest.
  @override
  Future<void> prompt(
    String text, {
    String delivery = 'queue',
    List<FileAttachment> files = const [],
  }) async {
    await _client.prompt(sessionID, text, delivery: delivery, files: files);
    unawaited(reconcile());
  }

  @override
  Future<void> interrupt() => _client.interrupt(sessionID);

  @override
  Future<void> setModel(
    String providerID,
    String modelID, {
    String? variant,
  }) async {
    await _client.setModel(sessionID, providerID, modelID, variant: variant);
    unawaited(reconcile());
  }

  @override
  Future<void> setAgent(String agent) async {
    await _client.setAgent(sessionID, agent);
    unawaited(reconcile());
  }

  Future<void> rename(String title) async {
    await _client.renameSession(sessionID, title);
    unawaited(reconcile());
  }

  void dispose() {
    _closed = true;
    _eventSub?.cancel();
    _reconnectSub?.cancel();
    _stream.close();
  }
}

/// Live state for one session. Auto-dispose: leaving the screen tears the
/// controller down; re-entering reconciles from scratch.
final sessionStateProvider = StreamProvider.autoDispose
    .family<SessionState, String>((ref, sessionID) {
      final client = ref.watch(apiClientProvider);
      if (client == null) {
        return const Stream<SessionState>.empty();
      }
      final connection = ref.watch(connectionControllerProvider);
      final controller = SessionController(
        client: client,
        sessionID: sessionID,
        events: connection?.events ?? const Stream<ApiEvent>.empty(),
        reconnectSignal: connection?.reconnectSignal,
      );
      ref.onDispose(() {
        controller.dispose();
        if (identical(sessionControllerRegistry[sessionID], controller)) {
          sessionControllerRegistry.remove(sessionID);
        }
      });
      sessionControllerRegistry[sessionID] = controller;
      unawaited(controller.start());
      return controller.stream;
    });

/// The controller behind [sessionStateProvider], for actions (prompt,
/// interrupt, model/agent switch). Read, not watch — actions are imperative.
/// Covers both transports: the ACP controller when its projection is live,
/// the HTTP controller otherwise.
SessionBackend? sessionControllerOf(Ref ref, String sessionID) {
  final acp = ref.read(acpConnectionProvider);
  final acpController = acp?.controllers[sessionID];
  if (acpController != null) return acpController;
  return ref.read(sessionStateProvider(sessionID)) is AsyncError
      ? null
      : sessionControllerRegistry[sessionID];
}

/// Widget-facing variant of [sessionControllerOf] (a WidgetRef reads
/// providers the same way; only the static type differs).
SessionBackend? sessionBackendOf(WidgetRef ref, String sessionID) {
  final acp = ref.read(acpConnectionProvider);
  final acpController = acp?.controllers[sessionID];
  if (acpController != null) return acpController;
  return sessionControllerRegistry[sessionID];
}

/// Registry of live HTTP controllers, so imperative actions (send,
/// interrupt) can find the controller for a session without watching
/// providers.
final Map<String, SessionBackend> sessionControllerRegistry =
    <String, SessionBackend>{};

/// The live ACP connection, or null when not in ACP mode. Watches the
/// connection state so it appears and disappears with it.
final acpConnectionProvider = Provider<AcpConnection?>((ref) {
  ref.watch(connectionProvider);
  return ref.read(connectionProvider.notifier).controller?.acp;
});

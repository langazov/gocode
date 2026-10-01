import 'dart:async';

import '../../features/session/timeline.dart';
import '../api/models.dart';
import 'client.dart';
import 'connection.dart';
import 'projection.dart';

export '../api/models.dart' show FileAttachment;

/// Drives one ACP session's [SessionState] stream: the setup state
/// (config options, modes), live updates through the projection, and the
/// v1 prompt lifecycle (the request resolves when the turn ends).
///
/// The projection is append-only across reconnects: ACP has no durable
/// history API to re-fetch, and a resume replays the full history before
/// its response, so a new [AcpSessionController] is built per attach
/// rather than reconciled.
class AcpSessionController implements SessionBackend {
  AcpSessionController({
    required AcpClient client,
    this.connection,
    required this.sessionID,
    required this.cwd,
    required Session session,
    Stream<AcpSessionUpdate>? updates,
  }) : _client = client,
       _updates = updates ?? client.updates,
       projection = AcpProjection(session);

  final AcpClient _client;

  /// The owning connection, for run/tool activity glue. Optional so tests
  /// can drive a controller against a bare client.
  final AcpConnection? connection;
  @override
  final String sessionID;
  final String cwd;
  final Stream<AcpSessionUpdate> _updates;

  final AcpProjection projection;

  final _stream = StreamController<SessionState>.broadcast();
  @override
  Stream<SessionState> get stream => _stream.stream;

  StreamSubscription<AcpSessionUpdate>? _sub;
  SessionState? _state;
  bool _closed = false;
  bool _busy = false;

  /// The last config options reported for this session.
  List<AcpConfigOption> configOptions = const [];

  /// The last modes (agents) reported for this session.
  List<AcpMode> modes = const [];

  /// The session as last emitted (title, agent, model follow the updates).
  Session get currentSession => _state?.session ?? projection.session;

  /// Resolves once the setup state (config options) has arrived.
  final Completer<void> setupComplete = Completer<void>();

  /// Attaches to the session's update stream and waits for the first state.
  Future<void> start() async {
    _sub = _updates
        .where((u) => u.sessionID == sessionID)
        .listen(_onUpdate, onError: (Object _) {});
    _emit();
  }

  /// Adopts the setup result of a session/new or session/load call.
  void adoptSetup(AcpSetupResult setup) {
    configOptions = setup.configOptions;
    modes = setup.modes;
    if (!setupComplete.isCompleted) setupComplete.complete();
    _emit();
  }

  void _onUpdate(AcpSessionUpdate update) {
    final changed = projection.apply(update);
    switch (update.kind) {
      case 'tool_call':
        connection?.toolEvent(sessionID, update.kind);
        if (changed) _emit();
      case 'tool_call_update':
        final status = update.raw['status'] as String?;
        connection?.toolEvent(
          sessionID,
          update.kind,
          settled: status == 'completed' ||
              status == 'failed' ||
              status == 'cancelled',
        );
        if (changed) _emit();
      case 'usage_update':
        // Nothing timeline-visible; the composer reads cost from stats.
        break;
      default:
        if (changed) _emit();
    }
  }

  void _emit() {
    if (_closed) return;
    _state = SessionState(
      session: _sessionWithTitle,
      items: projection.items,
      busy: _busy,
      todos: projection.todos,
      acpConfigOptions: configOptions,
    );
    _stream.add(_state!);
  }

  Session get _sessionWithTitle {
    final base = projection.session;
    final title = projection.title ?? base.title;
    if (identical(title, base.title)) {
      return base;
    }
    return Session(
      id: base.id,
      title: title,
      directory: base.directory,
      projectID: base.projectID,
      parentID: base.parentID,
      agent: _agentFromOptions ?? base.agent,
      model: _modelFromOptions ?? base.model,
      version: base.version,
      timeCreated: base.timeCreated,
      timeUpdated: DateTime.now().millisecondsSinceEpoch,
    );
  }

  String? get _agentFromOptions {
    for (final option in configOptions) {
      if (option.id == 'mode' && option.currentValue is String) {
        return option.currentValue as String;
      }
    }
    return null;
  }

  ModelRef? get _modelFromOptions {
    for (final option in configOptions) {
      if (option.id == 'model' && option.currentValue is String) {
        final ref = option.currentValue as String;
        final slash = ref.indexOf('/');
        if (slash > 0 && slash < ref.length - 1) {
          return ModelRef(
            providerID: ref.substring(0, slash),
            id: ref.substring(slash + 1),
            variant: _variantFromOptions,
          );
        }
      }
    }
    return null;
  }

  String? get _variantFromOptions {
    for (final option in configOptions) {
      if (option.id == 'thought_level' &&
          option.currentValue is String &&
          option.currentValue != 'default') {
        return option.currentValue as String;
      }
    }
    return null;
  }

  /// Sends a prompt. The returned future resolves when the turn ends (v1),
  /// carrying the stop reason; the timeline updates have already streamed.
  @override
  Future<void> prompt(
    String text, {
    String delivery = 'queue',
    List<FileAttachment> files = const [],
  }) async {
    _busy = true;
    connection?.runStarted(sessionID);
    _emit();
    try {
      final result = await _client.prompt(sessionID, text, files: files);
      connection?.runEnded(sessionID, result.stopReason);
      projection.lastStopReason = result.stopReason;
    } finally {
      _busy = false;
      _emit();
    }
  }

  /// Queues a follow-up without awaiting the turn: v1 has one prompt
  /// request per turn, so a prompt sent while busy is steered by the agent
  /// and settles with the running turn.
  Future<void> promptQueued(String text) => prompt(text);

  @override
  Future<void> interrupt() async {
    _client.cancel(sessionID);
  }

  @override
  Future<void> setAgent(String agent) async {
    configOptions = await _client.setConfigOption(sessionID, 'mode', agent);
    _emit();
  }

  @override
  Future<void> setModel(String providerID, String modelID, {String? variant}) async {
    configOptions = await _client.setConfigOption(
      sessionID,
      'model',
      '$providerID/$modelID',
    );
    if (variant != null) {
      configOptions = await _client.setConfigOption(
        sessionID,
        'thought_level',
        variant,
      );
    }
    _emit();
  }

  Future<void> setVariant(String? variant) async {
    configOptions = await _client.setConfigOption(
      sessionID,
      'thought_level',
      variant ?? 'default',
    );
    _emit();
  }

  Future<void> setAutoApprove(bool value) async {
    configOptions = await _client.setConfigOption(
      sessionID,
      'auto_approve',
      value,
      type: 'boolean',
    );
    _emit();
  }

  void dispose() {
    _closed = true;
    _sub?.cancel();
    _stream.close();
  }
}

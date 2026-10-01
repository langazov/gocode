import 'dart:async';

import '../../features/session/timeline.dart';
import '../api/models.dart' hide Provider;
import 'client.dart';
import 'session_controller.dart';

/// The app's single ACP attachment: one `gocode acp` process, the sessions
/// it serves, and the glue that lets HTTP-era consumers (sidebar activity,
/// asks) work unchanged.
///
/// ACP has no durable re-fetch API — replay happens once, inside
/// session/load, before its response — so there is no reconcile-on-
/// reconnect here by design: a dead process ends the connection, exactly
/// like a dead server would.
class AcpConnection {
  AcpConnection(this.client);

  final AcpClient client;

  /// The sessions the sidebar lists, decoded from session/list.
  List<Session> sessions = const [];

  /// Open sessions by id: one controller each, holding its projection.
  final controllers = <String, AcpSessionController>{};

  /// ACP updates translated into the ApiEvent shapes the app already
  /// consumes (run lifecycle, tool activity), so the sidebar's live dots
  /// and any other event-driven UI work over this transport too.
  final _events = StreamController<ApiEvent>.broadcast();
  Stream<ApiEvent> get events => _events.stream;

  /// Fires when the session list may have changed (a run ended, a delete).
  final _sessionsChanged = StreamController<void>.broadcast();
  Stream<void> get sessionsChanged => _sessionsChanged.stream;

  Stream<AcpSessionUpdate> get updates => client.updates;

  Stream<AcpPermissionRequest> get permissionRequests =>
      client.permissionRequests;

  Future<void> replyPermission(
    AcpPermissionRequest request,
    String optionId,
  ) => client.replyPermission(request, optionId);

  bool _closed = false;

  /// The modes and config options of the most recent session setup — the
  /// closest thing to a catalog ACP offers, used by pickers before any
  /// session exists this run.
  List<AcpMode> modes = const [];
  List<AcpConfigOption> configOptions = const [];

  /// Live state for one session: opens it (session/load, full replay) on
  /// first listen, then streams its controller's state. Re-listening
  /// reuses the open controller — the projection survives screen changes.
  Stream<SessionState> sessionStream(String sessionID) async* {
    var controller = controllers[sessionID];
    controller ??= await open(sessionID);
    yield* controller.stream;
  }

  // ------------------------------------------------------------------ lifecycle

  /// Refreshes the session list from session/list (all pages), skipping
  /// subagent sessions the same way the HTTP list does.
  Future<List<Session>> refreshSessions() async {
    final entries = await client.listSessions();
    final live = <String>[];
    final out = <Session>[];
    for (final entry in entries) {
      live.add(entry.id);
      out.add(_sessionOf(entry));
    }
    // Sessions opened since the last refresh keep their richer state.
    sessions = out;
    return out;
  }

  Session _sessionOf(AcpSessionEntry entry) {
    final controller = controllers[entry.id];
    if (controller != null) {
      final session = controller.currentSession;
      return Session(
        id: session.id,
        title: session.title,
        directory: session.directory,
        projectID: session.projectID,
        parentID: session.parentID,
        agent: session.agent,
        model: session.model,
        version: session.version,
        timeCreated: session.timeCreated,
        timeUpdated: entry.updatedAt.millisecondsSinceEpoch,
      );
    }
    return Session(
      id: entry.id,
      title: entry.title ?? 'Untitled',
      directory: entry.cwd,
      timeCreated: entry.updatedAt.millisecondsSinceEpoch,
      timeUpdated: entry.updatedAt.millisecondsSinceEpoch,
    );
  }

  Session sessionFor(String id) {
    for (final session in sessions) {
      if (session.id == id) return session;
    }
    final controller = controllers[id];
    return controller?.currentSession ??
        Session(
          id: id,
          title: 'Session',
          directory: '',
          timeCreated: 0,
          timeUpdated: 0,
        );
  }

  /// session/new: opens and registers the controller, returns it.
  Future<AcpSessionController> createSession(String cwd) async {
    final setup = await client.newSession(cwd);
    final session = Session(
      id: setup.sessionID,
      title: 'Untitled',
      directory: cwd,
      timeCreated: DateTime.now().millisecondsSinceEpoch,
      timeUpdated: DateTime.now().millisecondsSinceEpoch,
    );
    return _register(session, setup);
  }

  /// session/load: attaches with a full replay, so the returned controller
  /// already holds the complete timeline.
  Future<AcpSessionController> open(String sessionID) async {
    final existing = controllers[sessionID];
    if (existing != null) return existing;

    var cwd = sessionFor(sessionID).directory;
    if (cwd.isEmpty && sessions.isEmpty) {
      await refreshSessions();
      cwd = sessionFor(sessionID).directory;
    }
    if (cwd.isEmpty) {
      throw StateError('unknown working directory for session $sessionID');
    }

    // The controller must be listening before the load runs: the replay
    // streams as session/update notifications ahead of the response, and a
    // broadcast stream does not buffer them.
    final stub = sessionFor(sessionID);
    final controller = AcpSessionController(
      client: client,
      connection: this,
      sessionID: sessionID,
      cwd: cwd,
      session: stub,
    );
    controllers[sessionID] = controller;
    await controller.start();
    final setup = await client.loadSession(sessionID, cwd);
    controller.adoptSetup(setup);
    modes = setup.modes;
    configOptions = setup.configOptions;
    return controller;
  }

  AcpSessionController _register(Session session, AcpSetupResult setup) {
    modes = setup.modes;
    configOptions = setup.configOptions;
    final controller = AcpSessionController(
      client: client,
      connection: this,
      sessionID: setup.sessionID,
      cwd: session.directory,
      session: session,
    );
    controller.adoptSetup(setup);
    controllers[setup.sessionID] = controller;
    unawaited(controller.start());
    _sessionsChanged.add(null);
    return controller;
  }

  Future<void> deleteSession(String sessionID) async {
    final controller = controllers.remove(sessionID);
    controller?.dispose();
    await client.deleteSession(sessionID);
    await refreshSessions();
    _sessionsChanged.add(null);
  }

  // ------------------------------------------------------- run & activity glue

  /// Called by a session controller as its prompt request goes out: the v1
  /// request lifetime is the run lifetime.
  void runStarted(String sessionID) {
    _emit(sessionID, 'session.next.run.started');
  }

  /// Called when the prompt request resolves with the turn's stop reason.
  void runEnded(String sessionID, String stopReason) {
    _emit(sessionID, 'session.next.run.ended');
    unawaited(_refreshAfterRun());
  }

  /// Tool activity, translated to the event names the activity tracker
  /// knows: a call starting (or its title refreshing) is "called", a
  /// settlement is "success" (failed settlements land here too — the dot
  /// only needs "no longer running").
  void toolEvent(String sessionID, String update, {bool settled = false}) {
    _emit(
      sessionID,
      settled
          ? 'session.next.tool.success'
          : 'session.next.tool.called',
      settled ? const {} : const {'tool': 'tool'},
    );
  }

  Future<void> _refreshAfterRun() async {
    if (_closed) return;
    try {
      await refreshSessions();
      _sessionsChanged.add(null);
    } catch (_) {
      // The list stays as it was; the next run retries.
    }
  }

  void _emit(String sessionID, String type, [Map<String, dynamic> data = const {}]) {
    _events.add(
      ApiEvent(
        id: 'acp_${_eventCounter++}',
        type: type,
        data: data,
        sessionID: sessionID,
      ),
    );
  }

  int _eventCounter = 0;

  Future<void> stop() async {
    if (_closed) return;
    _closed = true;
    for (final controller in controllers.values) {
      controller.dispose();
    }
    controllers.clear();
    await client.stop();
    if (!_events.isClosed) await _events.close();
    if (!_sessionsChanged.isClosed) await _sessionsChanged.close();
  }
}

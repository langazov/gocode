import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:meta/meta.dart';

import '../api/models.dart';
import '../process/supervisor.dart' show ShellEnvironment;
import 'json_rpc.dart';
import 'protocol.dart';

export 'json_rpc.dart' show JsonRpcException;
export 'protocol.dart'
    show
        AcpConfigOption,
        AcpMode,
        AcpOptionGroup,
        AcpOptionValue,
        AcpPermissionOption,
        AcpPermissionRequest,
        AcpSessionEntry,
        AcpSessionUpdate,
        AcpToolLocation;

/// Agent info reported by initialize.
class AcpAgentInfo {
  const AcpAgentInfo({
    required this.name,
    required this.version,
    required this.protocolVersion,
    this.capabilities = const {},
  });

  final String name;
  final String version;
  final int protocolVersion;
  final Map<String, dynamic> capabilities;
}

/// Result of session/new / session/load: the session id plus the setup
/// state the agent returned.
class AcpSetupResult {
  const AcpSetupResult({
    required this.sessionID,
    required this.configOptions,
    required this.modes,
  });

  final String sessionID;
  final List<AcpConfigOption> configOptions;
  final List<AcpMode> modes;
}

/// Result of session/prompt — the turn's stop reason, and the id the user
/// message will carry in updates and replays (gocode extension, in _meta).
class AcpPromptResult {
  const AcpPromptResult({required this.stopReason, required this.messageID});

  final String stopReason;
  final String messageID;
}

/// Drives one `gocode acp` subprocess as an ACP v1 client.
///
/// Lifecycle: [start] launches the process and negotiates initialize; from
/// then on every session/update notification flows out of [updates] and
/// every permission ask arrives on [permissionRequests] until answered
/// through [replyPermission]. The protocol is v1: a session/prompt request
/// stays open for the whole turn and resolves with the turn's stop reason
/// (agentclientprotocol.com/protocol/v1/prompt-turn).
class AcpClient {
  AcpClient({this.onLog});

  /// Diagnostics sink (stderr lines, protocol failures); never stdout,
  /// which belongs to the protocol.
  final void Function(String message)? onLog;

  JsonRpcConnection? _conn;
  Process? _process;
  AcpAgentInfo? agentInfo;

  StreamSubscription<JsonRpcNotification>? _notifySub;
  StreamSubscription<String>? _stderrSub;

  final _updatesController = StreamController<AcpSessionUpdate>.broadcast();
  final _permissionsController =
      StreamController<AcpPermissionRequest>.broadcast();

  /// Outstanding permission asks awaiting a [replyPermission].
  final _pendingPermissions =
      <AcpPermissionRequest, Completer<Map<String, dynamic>>>{};

  /// Every session/update notification, post-envelope (sessionId folded in).
  Stream<AcpSessionUpdate> get updates => _updatesController.stream;

  /// Every permission ask from the agent, in arrival order. One ask is
  /// live at a time per session; the agent serialises them itself.
  Stream<AcpPermissionRequest> get permissionRequests =>
      _permissionsController.stream;

  /// Spawns the agent binary and negotiates initialize over v1.
  Future<AcpAgentInfo> start({
    required String binaryPath,
    required String workingDirectory,
    String? model,
  }) async {
    if (_conn != null) {
      throw StateError('AcpClient already started');
    }

    final loginPath = await ShellEnvironment.path();
    final process = await Process.start(
      binaryPath,
      [
        'acp',
        '--cwd',
        workingDirectory,
        if (model != null && model.isNotEmpty) ...['--model', model],
      ],
      workingDirectory: workingDirectory,
      environment: {
        ...Platform.environment,
        // Apps launched from Finder/Dock get launchd's bare PATH; hand the
        // agent the login shell's so its LSP/MCP children resolve.
        'PATH': ?loginPath,
        'GOCODE_CLIENT': 'flutter',
      },
    );
    _process = process;
    _stderrSub = process.stderr
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen((line) => onLog?.call(line));
    unawaited(
      process.exitCode.then((code) {
        onLog?.call('gocode acp exited with code $code');
        _conn?.close();
      }),
    );
    return _connect(
      incoming: process.stdout
          .transform(utf8.decoder)
          .transform(const LineSplitter()),
      send: process.stdin.writeln,
    );
  }

  /// Builds the connection and negotiates initialize. The test entry point
  /// wires in-memory streams here.
  @visibleForTesting
  Future<AcpAgentInfo> connectForTest({
    required Stream<String> incoming,
    required void Function(String line) send,
  }) => _connect(incoming: incoming, send: send);

  Future<AcpAgentInfo> _connect({
    required Stream<String> incoming,
    required void Function(String line) send,
  }) async {
    final connection = JsonRpcConnection(incoming: incoming, send: send);
    _conn = connection;
    unawaited(connection.done.then((_) => onLog?.call('connection closed')));
    _notifySub = connection.notifications.listen(_onNotification);
    connection.handle('session/request_permission', _onPermissionRequest);
    connection.handle('elicitation/create', _onElicitation);

    final result = await connection.request('initialize', {
      'protocolVersion': 1,
      'clientCapabilities': const {
        'promptCapabilities': {'image': true},
      },
    });
    final map = result is Map<String, dynamic> ? result : const {};
    final info = map['agentInfo'] is Map<String, dynamic>
        ? map['agentInfo'] as Map<String, dynamic>
        : const <String, dynamic>{};
    final agentInfo = AcpAgentInfo(
      name: info['name'] as String? ?? 'gocode',
      version: info['version'] as String? ?? '',
      protocolVersion: (map['protocolVersion'] as num?)?.toInt() ?? 1,
      capabilities: map['agentCapabilities'] is Map<String, dynamic>
          ? map['agentCapabilities'] as Map<String, dynamic>
          : const {},
    );
    this.agentInfo = agentInfo;
    return agentInfo;
  }

  void _onNotification(JsonRpcNotification notification) {
    if (notification.method != 'session/update') return;
    final update = notification.params['update'];
    if (update is! Map<String, dynamic>) return;
    // Fold the envelope's sessionId into the update itself, so consumers
    // read one object.
    update['sessionId'] = notification.params['sessionId'];
    _updatesController.add(AcpSessionUpdate(update));
  }

  /// Elicitations are declined: this client has no browser surface for the
  /// OAuth URL flow, and the agent carries on without that MCP server.
  Future<Map<String, dynamic>> _onElicitation(Map<String, dynamic> params) =>
      Future.value(const {'action': 'decline'});

  Future<Map<String, dynamic>> _onPermissionRequest(
    Map<String, dynamic> params,
  ) {
    final toolCall = params['toolCall'] is Map<String, dynamic>
        ? params['toolCall'] as Map<String, dynamic>
        : const <String, dynamic>{};
    final request = AcpPermissionRequest(
      sessionID: params['sessionId'] as String? ?? '',
      options: (params['options'] as List<dynamic>? ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(AcpPermissionOption.fromJson)
          .toList(),
      toolCall: toolCall,
    );
    final completer = Completer<Map<String, dynamic>>();
    _pendingPermissions[request] = completer;
    _permissionsController.add(request);
    return completer.future;
  }

  /// Answers a pending ask. [optionId] is one of the offered options' ids
  /// ("once", "always", "reject" for gocode's agent).
  Future<void> replyPermission(AcpPermissionRequest request, String optionId) {
    final completer = _pendingPermissions.remove(request);
    if (completer != null && !completer.isCompleted) {
      completer.complete(
        <String, dynamic>{
          'outcome': <String, dynamic>{
            'outcome': 'selected',
            'optionId': optionId,
          },
        },
      );
    }
    return Future.value();
  }

  // ---------------------------------------------------------------- sessions

  /// session/new. The response carries configOptions and modes; the
  /// available_commands_update notification follows the response.
  Future<AcpSetupResult> newSession(String cwd) async {
    final result = await _request('session/new', {'cwd': cwd});
    return AcpSetupResult(
      sessionID: result['sessionId'] as String? ?? '',
      configOptions: _decodeConfigOptions(result),
      modes: _decodeModes(result),
    );
  }

  /// session/load — resume with a full history replay before the response,
  /// so once this resolves the timeline is complete.
  Future<AcpSetupResult> loadSession(String sessionID, String cwd) async {
    final result = await _request('session/load', {
      'sessionId': sessionID,
      'cwd': cwd,
    });
    return AcpSetupResult(
      sessionID: sessionID,
      configOptions: _decodeConfigOptions(result),
      modes: _decodeModes(result),
    );
  }

  /// session/list, all pages.
  Future<List<AcpSessionEntry>> listSessions({String? cwd}) async {
    final entries = <AcpSessionEntry>[];
    String? cursor;
    do {
      final result = await _request('session/list', {
        if (cwd != null && cwd.isNotEmpty) 'cwd': cwd,
        'cursor': ?cursor,
      });
      entries.addAll(
        (result['sessions'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(AcpSessionEntry.fromJson),
      );
      cursor = result['nextCursor'] as String?;
    } while (cursor != null && cursor.isNotEmpty);
    return entries;
  }

  Future<void> deleteSession(String sessionID) async {
    await _request('session/delete', {'sessionId': sessionID});
  }

  /// session/cancel — notification; the prompt's own response carries the
  /// cancelled stop reason.
  void cancel(String sessionID) {
    _conn?.notify('session/cancel', {'sessionId': sessionID});
  }

  // ------------------------------------------------------------------ config

  /// session/set_config_option — mode, model, thought level, auto-approve.
  Future<List<AcpConfigOption>> setConfigOption(
    String sessionID,
    String optionId,
    Object value, {
    String? type,
  }) async {
    final result = await _request('session/set_config_option', {
      'sessionId': sessionID,
      'configId': optionId,
      'type': ?type,
      'value': value,
    });
    return _decodeConfigOptions(result);
  }

  /// session/set_mode — the v1 way to switch agent.
  Future<void> setMode(String sessionID, String modeID) async {
    await _request('session/set_mode', {
      'sessionId': sessionID,
      'modeId': modeID,
    });
  }

  // ------------------------------------------------------------------ prompt

  /// session/prompt with text (and image attachments). In v1 the request
  /// resolves when the whole turn ends.
  Future<AcpPromptResult> prompt(
    String sessionID,
    String text, {
    List<FileAttachment> files = const [],
  }) async {
    final blocks = <Map<String, dynamic>>[
      {'type': 'text', 'text': text},
      for (final file in files)
        if (file.uri case final uri?)
          switch (_asImageBlock(uri, file.name)) {
            final block? => block,
            null => {
                'type': 'resource_link',
                'uri': uri,
                'name': file.name ?? uri,
              },
          },
    ];
    final result = await _request('session/prompt', {
      'sessionId': sessionID,
      'prompt': blocks,
    });
    final meta = result['_meta'];
    return AcpPromptResult(
      stopReason: result['stopReason'] as String? ?? 'end_turn',
      messageID: meta is Map<String, dynamic> &&
              meta['gocode'] is Map<String, dynamic> &&
              (meta['gocode'] as Map<String, dynamic>)['userMessageId'] is String
          ? (meta['gocode'] as Map<String, dynamic>)['userMessageId'] as String
          : '',
    );
  }

  static Map<String, dynamic>? _asImageBlock(String uri, String? name) {
    if (!uri.startsWith('data:')) return null;
    final rest = uri.substring(5);
    final comma = rest.indexOf(',');
    if (comma < 0) return null;
    final head = rest.substring(0, comma);
    if (!head.endsWith(';base64')) return null;
    final mime = head.substring(0, head.length - 7);
    if (!mime.startsWith('image/')) return null;
    return {'type': 'image', 'mimeType': mime, 'data': rest.substring(comma + 1)};
  }

  // ------------------------------------------------------------------ helpers

  Future<dynamic> _request(String method, [Object? params]) async {
    final conn = _conn;
    if (conn == null) {
      throw StateError('ACP connection is not open');
    }
    return conn.request(method, params);
  }

  static List<AcpConfigOption> _decodeConfigOptions(Map<String, dynamic> raw) {
    return (raw['configOptions'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(AcpConfigOption.fromJson)
        .toList();
  }

  static List<AcpMode> _decodeModes(Map<String, dynamic> raw) {
    final modes = raw['modes'];
    if (modes is! Map<String, dynamic>) return const [];
    return (modes['availableModes'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(AcpMode.fromJson)
        .toList();
  }

  /// Kills the subprocess and closes the connection. Pending prompts fail;
  /// unanswered permission asks are left to the agent's own cleanup (the
  /// process is dying with the connection).
  Future<void> stop() async {
    final conn = _conn;
    final process = _process;
    _conn = null;
    _process = null;
    conn?.close();
    await _notifySub?.cancel();
    _notifySub = null;
    await _stderrSub?.cancel();
    _stderrSub = null;
    if (process != null) {
      try {
        await process.stdin.close().timeout(const Duration(seconds: 2));
      } catch (_) {}
      process.kill();
      await process.exitCode.timeout(
        const Duration(seconds: 5),
        onTimeout: () => -1,
      ).catchError((_) => -1);
    }
    for (final completer in _pendingPermissions.values) {
      if (!completer.isCompleted) {
        completer.complete(
          const {
            'outcome': {'outcome': 'cancelled'},
          },
        );
      }
    }
    _pendingPermissions.clear();
    if (!_updatesController.isClosed) await _updatesController.close();
    if (!_permissionsController.isClosed) await _permissionsController.close();
  }
}

import 'dart:async';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/client.dart';
import '../api/models.dart' hide Provider;
import '../api/sse.dart';
import '../process/supervisor.dart';

/// How the app talks to a gocode server.
enum ConnectionMode {
  /// Desktop: spawn `gocode serve` as a child process.
  local,

  /// Mobile (or advanced): attach to a running server.
  remote,
}

class ConnectionSettings {
  const ConnectionSettings({
    this.mode = ConnectionMode.local,
    this.binaryPath = '',
    this.workingDirectory = '',
    this.remoteUrl = '',
    this.remoteUsername,
    this.remotePassword,
  });

  final ConnectionMode mode;
  final String binaryPath;
  final String workingDirectory;
  final String remoteUrl;
  final String? remoteUsername;
  final String? remotePassword;

  ConnectionSettings copyWith({
    ConnectionMode? mode,
    String? binaryPath,
    String? workingDirectory,
    String? remoteUrl,
    String? remoteUsername,
    String? remotePassword,
  }) =>
      ConnectionSettings(
        mode: mode ?? this.mode,
        binaryPath: binaryPath ?? this.binaryPath,
        workingDirectory: workingDirectory ?? this.workingDirectory,
        remoteUrl: remoteUrl ?? this.remoteUrl,
        remoteUsername: remoteUsername ?? this.remoteUsername,
        remotePassword: remotePassword ?? this.remotePassword,
      );

  static Future<ConnectionSettings> load() async {
    final prefs = await SharedPreferences.getInstance();
    final modeName = prefs.getString('connection.mode');
    return ConnectionSettings(
      mode: modeName == 'remote' ? ConnectionMode.remote : ConnectionMode.local,
      binaryPath: prefs.getString('connection.binaryPath') ?? '',
      workingDirectory: prefs.getString('connection.workingDirectory') ?? '',
      remoteUrl: prefs.getString('connection.remoteUrl') ?? '',
      remoteUsername: prefs.getString('connection.remoteUsername'),
      remotePassword: prefs.getString('connection.remotePassword'),
    );
  }

  Future<void> save() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString('connection.mode', mode.name);
    await prefs.setString('connection.binaryPath', binaryPath);
    await prefs.setString('connection.workingDirectory', workingDirectory);
    await prefs.setString('connection.remoteUrl', remoteUrl);
    if (remoteUsername != null) {
      await prefs.setString('connection.remoteUsername', remoteUsername!);
    }
    if (remotePassword != null) {
      await prefs.setString('connection.remotePassword', remotePassword!);
    }
  }
}

enum ConnectionPhase { disconnected, connecting, connected, error }

class ConnectionState {
  const ConnectionState({
    this.phase = ConnectionPhase.disconnected,
    this.baseUrl,
    this.error,
    this.stderr = const [],
  });

  final ConnectionPhase phase;
  final String? baseUrl;
  final String? error;
  final List<String> stderr;

  bool get isConnected => phase == ConnectionPhase.connected;
}

/// Owns the supervisor (local mode) and the API client + SSE feed.
///
/// Not a provider itself — it's the mutable engine behind
/// [connectionControllerProvider]. Kept out of the widget tree so the SSE
/// subscription survives screen changes (and Riverpod 3's pausing of
/// out-of-view providers).
class ConnectionController {
  ConnectionController(this._settings);

  final ConnectionSettings _settings;

  ServerSupervisor? _supervisor;
  GocodeClient? _client;
  SseClient? _sse;

  /// The SSE feed's reconnect signal — owners reconcile on every emission.
  Stream<void>? get reconnectSignal => _sse?.reconnectSignal;

  Stream<ApiEvent> get events => _eventBus.stream;

  // Sync, so AppConnection's state is current by the time connect() (and
  // hence apply()) completes, rather than a microtask later.
  final _state = StreamController<ConnectionState>.broadcast(sync: true);
  Stream<ConnectionState> get state => _state.stream;

  final _eventBus = StreamController<ApiEvent>.broadcast();

  /// Every committed event from every connected session.
  // (getter defined above, next to reconnectSignal)

  final _log = <String>[];

  GocodeClient? get client => _client;
  String? get baseUrl => _client?.baseUrl;

  Future<void> connect() async {
    if (_client != null) return;
    _emit(ConnectionState(
      phase: ConnectionPhase.connecting,
      stderr: List.of(_log),
    ));

    try {
      String url;
      if (_settings.mode == ConnectionMode.local) {
        if (!Platform.isMacOS && !Platform.isLinux && !Platform.isWindows) {
          throw StateError(
            'local mode requires a desktop platform; use remote attach',
          );
        }
        final dir = _settings.workingDirectory;
        if (dir.isEmpty || !await Directory(dir).exists()) {
          throw StateError('working directory not set or missing: $dir');
        }
        var binary = _settings.binaryPath;
        if (binary.isEmpty || await BinaryLocator.verify(binary) == null) {
          binary = await BinaryLocator.find() ?? '';
        }
        if (binary.isEmpty) {
          throw StateError(
            'gocode binary not found — set its path in Settings',
          );
        }
        final supervisor = ServerSupervisor(
          binaryPath: binary,
          workingDirectory: dir,
          onStderr: _appendLog,
          onStdout: _appendLog,
        );
        _supervisor = supervisor;
        url = await supervisor.start();
      } else {
        url = _settings.remoteUrl;
        if (url.isEmpty) {
          throw StateError('remote URL not set');
        }
      }

      final client = GocodeClient(
        baseUrl: url,
        username: _settings.remoteUsername,
        password: _settings.remotePassword,
      );
      if (!await client.health()) {
        throw StateError('server at $url is not healthy');
      }
      _client = client;

      final sse = SseClient(
        baseUrl: url,
        username: _settings.remoteUsername,
        password: _settings.remotePassword,
      );
      _sse = sse;
      sse.events.listen(_eventBus.add);
      await sse.start();

      _emit(ConnectionState(
        phase: ConnectionPhase.connected,
        baseUrl: url,
        stderr: List.of(_log),
      ));
    } catch (e) {
      _emit(ConnectionState(
        phase: ConnectionPhase.error,
        error: e.toString(),
        stderr: List.of(_log),
      ));
      rethrow;
    }
  }

  void _appendLog(String line) {
    _log.add(line);
    if (_log.length > 500) _log.removeRange(0, _log.length - 500);
  }

  void _emit(ConnectionState s) {
    if (!_state.isClosed) _state.add(s);
  }

  Future<void> disconnect() async {
    _sse?.close();
    _sse = null;
    _client?.close();
    _client = null;
    await _supervisor?.stop();
    _supervisor = null;
    _emit(const ConnectionState());
  }

  void dispose() {
    disconnect();
    _state.close();
    _eventBus.close();
  }
}

/// Settings are shared, mutable app-level state.
class SettingsNotifier extends Notifier<ConnectionSettings> {
  @override
  ConnectionSettings build() => const ConnectionSettings();

  Future<void> load() async {
    state = await ConnectionSettings.load();
  }

  void update(ConnectionSettings settings) {
    state = settings;
    unawaited(settings.save());
  }
}

final settingsProvider =
    NotifierProvider<SettingsNotifier, ConnectionSettings>(
  SettingsNotifier.new,
);

/// Owns the app's single connection and exposes its state.
///
/// Not watching [settingsProvider] deliberately: the settings screen edits a
/// draft and calls [apply] once, so typing doesn't tear the server down on
/// every keystroke.
class AppConnection extends Notifier<ConnectionState> {
  ConnectionController? _controller;
  StreamSubscription<ConnectionState>? _sub;

  @override
  ConnectionState build() {
    ref.onDispose(() {
      unawaited(_sub?.cancel());
      _controller?.dispose();
      _controller = null;
    });
    return const ConnectionState();
  }

  ConnectionController? get controller => _controller;

  /// The live API client, or null when disconnected.
  GocodeClient? get client => _controller?.client;

  /// Connects using [settings]. Replaces any existing connection.
  Future<void> apply(ConnectionSettings settings) async {
    await _teardown();
    final controller = ConnectionController(settings);
    _controller = controller;
    _sub = controller.state.listen((s) => state = s);
    try {
      await controller.connect();
    } catch (_) {
      // The stream already emitted the error state; swallow the rethrow so
      // callers can read state instead of try/catching every call. Catches
      // Object because connect() raises StateError (an Error, not an
      // Exception) for configuration problems.
    }
  }

  /// Connects with the current settings (first launch / retry).
  Future<void> connect() => apply(ref.read(settingsProvider));

  Future<void> disconnect() async {
    await _teardown();
    state = const ConnectionState();
  }

  Future<void> _teardown() async {
    await _sub?.cancel();
    _sub = null;
    _controller?.dispose();
    _controller = null;
  }
}

final connectionProvider =
    NotifierProvider<AppConnection, ConnectionState>(AppConnection.new);

/// The live controller, or null. Watch this to rebuild when the connection
/// comes up or drops.
///
/// Watches the connection *state*: the notifier's identity never changes, so
/// watching `connectionProvider.notifier` would compute once — typically at
/// launch, before any connection — and pin null forever. Dependents are only
/// notified when the returned instance actually changes.
final connectionControllerProvider = Provider<ConnectionController?>((ref) {
  ref.watch(connectionProvider);
  return ref.read(connectionProvider.notifier).controller;
});

/// The live client, or null. The client appears later than its controller
/// (after the server is healthy), so this watches the state for itself.
final apiClientProvider = Provider<GocodeClient?>((ref) {
  ref.watch(connectionProvider);
  return ref.read(connectionProvider.notifier).client;
});

/// The merged event stream across all connected sessions.
final eventBusProvider = StreamProvider<ApiEvent>((ref) {
  final controller = ref.watch(connectionControllerProvider);
  return controller?.events ?? const Stream<ApiEvent>.empty();
});

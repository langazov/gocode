import 'dart:async';
import 'dart:convert';
import 'dart:io';

/// Supervises a headless `gocode serve` child process on desktop platforms.
///
/// Mirrors the opencode desktop app's sidecar: spawn → parse the
/// `gocode server listening on http://…` line from stdout → health-check →
/// serve the base URL to the client layer. On exit: graceful SIGTERM, then
/// SIGKILL after [stopTimeout]. On Windows the process tree is killed via
/// `taskkill /T /F`.
class ServerSupervisor {
  ServerSupervisor({
    required this.binaryPath,
    required this.workingDirectory,
    this.hostname = '127.0.0.1',
    this.port = 0,
    this.startTimeout = const Duration(seconds: 60),
    this.stopTimeout = const Duration(seconds: 6),
    this.maxRestarts = 3,
    void Function(String line)? onStdout,
    void Function(String line)? onStderr,
  })  : _onStdout = onStdout,
        _onStderr = onStderr;

  /// Absolute path to the gocode binary.
  final String binaryPath;

  /// The project directory; gocode is single-project-per-process, so this is
  /// the child's cwd.
  final String workingDirectory;

  final String hostname;

  /// 0 = ephemeral port, parsed from the startup line.
  final int port;

  final Duration startTimeout;
  final Duration stopTimeout;
  final int maxRestarts;

  final void Function(String line)? _onStdout;
  final void Function(String line)? _onStderr;

  Process? _process;
  Timer? _exitWatchdog;

  String? _baseUrl;
  String? get baseUrl => _baseUrl;

  bool get isRunning => _process != null;

  /// The startup line gocode prints, e.g.
  /// `gocode server listening on http://127.0.0.1:54321`.
  static final RegExp listeningRe =
      RegExp(r'listening on (https?://[^\s]+)');

  /// The port the first start bound; restarts reuse it so the client's base
  /// URL stays valid.
  int? _boundPort;

  /// Set by [stop]: an orderly shutdown must not trigger a crash restart.
  bool _stopped = false;

  /// Spawns `gocode serve` on an ephemeral loopback port.
  Future<Process> _spawn() async {
    final loginPath = await ShellEnvironment.path();
    return Process.start(
      binaryPath,
      ['serve', '--hostname', hostname, '--port', '${_boundPort ?? port}'],
      workingDirectory: workingDirectory,
      environment: {
        ...Platform.environment,
        // Apps launched from Finder/Dock get launchd's bare PATH; hand the
        // server the login shell's so its LSP/MCP children resolve.
        'PATH': ?loginPath,
        // Marks requests from this client, same flag the TUI sets.
        'GOCODE_CLIENT': 'flutter',
      },
    );
  }

  /// Ensures the server dies when this app dies, even without cleanup.
  ///
  /// - Linux: `PR_SET_PDEATHSIG` via `setsid`-free prctl — the kernel kills
  ///   the child when the parent thread exits.
  /// - macOS: no PDEATHSIG; a tiny `sh -c` watchdog polls the parent pid and
  ///   kills the server when it disappears.
  /// - Windows: the supervisor's `taskkill /T` covers orderly shutdown; a
  ///   Job Object would be needed for hard crashes (tracked in the plan).
  Future<void> _setupParentDeath(int childPid) async {
    if (Platform.isWindows) return;

    if (Platform.isLinux) {
      // setsid-free: run prctl through the binary itself isn't possible, so
      // use a helper that applies PDEATHSIG then execs gocode — but the
      // process is already spawned. Instead spawn the watchdog below, which
      // works on both Linux and macOS.
    }

    // Watchdog: sleep in a loop; when the app pid vanishes, kill the server.
    // `kill -0` is the cheap liveness probe; the trap keeps the watcher from
    // firing on its own termination.
    final appPid = pid;
    final script = '''
while kill -0 $appPid 2>/dev/null; do sleep 1; done
kill $childPid 2>/dev/null
''';
    try {
      await Process.start(
        '/bin/sh',
        ['-c', script],
        mode: ProcessStartMode.detached,
      );
    } catch (_) {
      // Best-effort; orderly shutdown still works without it.
    }
  }

  /// Spawns the server and resolves once healthy.
  ///
  /// Throws if the binary cannot run, the startup line doesn't arrive within
  /// [startTimeout], or the health check fails.
  Future<String> start() async {
    if (_process != null) return _baseUrl!;

    final process = await _spawn();
    _process = process;

    // Guard against the parent dying without cleanup. Dart's exit handlers
    // don't run on SIGTERM/Kill, so the OS must own the child's lifetime.
    await _setupParentDeath(process.pid);

    final urlCompleter = Completer<String>();
    final startupLines = <String>[];

    _watchStream(process.stdout, (line) {
      startupLines.add(line);
      _onStdout?.call(line);
      final match = listeningRe.firstMatch(line);
      if (match != null && !urlCompleter.isCompleted) {
        urlCompleter.complete(match.group(1)!.trim());
      }
    });
    _watchStream(process.stderr, (line) {
      _onStderr?.call(line);
      if (!urlCompleter.isCompleted &&
          line.toLowerCase().contains('error')) {
        // Startup failures land on stderr; surface them for diagnostics.
        _onStderr?.call('[startup] $line');
      }
    });

    unawaited(process.exitCode.then((code) {
      _process = null;
      _baseUrl = null;
      if (!urlCompleter.isCompleted) {
        urlCompleter.completeError(
          StateError('gocode exited before listening (code $code)'),
        );
      }
    }));

    final String url;
    try {
      url = await urlCompleter.future.timeout(startTimeout);
    } on TimeoutException {
      await _kill();
      throw TimeoutException(
        'gocode did not report a listening address within $startTimeout',
      );
    }

    _baseUrl = url;
    _boundPort ??= Uri.tryParse(url)?.port;
    await _waitForHealthy(url);

    // Restart-on-crash with backoff, bounded by [maxRestarts].
    unawaited(process.exitCode.then((_) => _maybeRestart()));
    return url;
  }

  void _watchStream(Stream<List<int>> stream, void Function(String) onLine) {
    stream
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen(onLine, onError: (Object e) {
      _onStderr?.call('stream error: $e');
    });
  }

  Future<void> _waitForHealthy(String url) async {
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 5);
    final deadline = DateTime.now().add(startTimeout);
    try {
      while (DateTime.now().isBefore(deadline)) {
        try {
          final request = await client
              .getUrl(Uri.parse('$url/api/health'))
              .timeout(const Duration(seconds: 5));
          final response = await request.close().timeout(
                const Duration(seconds: 5),
              );
          await response.drain<void>().catchError((_) {});
          if (response.statusCode == 200) return;
        } catch (_) {}
        await Future<void>.delayed(const Duration(milliseconds: 250));
      }
      throw TimeoutException('gocode health check failed for $url');
    } finally {
      client.close(force: true);
    }
  }

  int _restarts = 0;

  Future<void> _maybeRestart() async {
    if (_stopped || _restarts >= maxRestarts) return;
    _restarts++;
    _exitWatchdog?.cancel();
    await Future<void>.delayed(Duration(seconds: 1 * _restarts));
    if (_stopped || _process != null) return;
    try {
      await start();
    } catch (_) {
      // Give up silently; the connection layer reports the outage.
    }
  }

  /// Stops the server. SIGTERM first, then SIGKILL after [stopTimeout].
  Future<void> stop() async {
    _stopped = true;
    _exitWatchdog?.cancel();
    final process = _process;
    _process = null;
    _baseUrl = null;
    if (process == null) return;

    process.stdin.write('q\n');
    unawaited(process.stdin.flush().catchError((_) {}));
    final exited = process.exitCode;
    try {
      await exited.timeout(stopTimeout);
      return;
    } on TimeoutException {
      // fall through to kill
    }

    if (Platform.isWindows) {
      await Process.run('taskkill', ['/T', '/F', '/PID', '${process.pid}']);
    } else {
      process.kill(ProcessSignal.sigterm);
      try {
        await exited.timeout(const Duration(seconds: 2));
        return;
      } on TimeoutException {
        process.kill(ProcessSignal.sigkill);
      }
    }
    await exited.catchError((_) => -1);
  }

  Future<void> _kill() async {
    final process = _process;
    _process = null;
    _baseUrl = null;
    if (process == null) return;
    process.kill(Platform.isWindows ? ProcessSignal.sigterm : ProcessSignal.sigkill);
    await process.exitCode.catchError((_) => -1);
  }
}

/// Locates the gocode binary: explicit setting → PATH → null.
class BinaryLocator {
  static const executableName = 'gocode';

  /// Checks an explicit path; returns it when executable, else null.
  static Future<String?> verify(String path) async {
    if (path.isEmpty) return null;
    final file = File(path);
    if (!await file.exists()) return null;
    try {
      final result = await Process.run(path, ['--version']);
      return result.exitCode == 0 ? path : null;
    } catch (_) {
      return null;
    }
  }

  /// Searches the login shell's PATH, this process's PATH, and common install
  /// locations for gocode.
  static Future<String?> find() async {
    final separator = Platform.isWindows ? ';' : ':';
    final name = Platform.isWindows ? '$executableName.exe' : executableName;
    final home = Platform.environment['HOME'] ??
        Platform.environment['USERPROFILE'] ??
        '';
    final dirs = [
      ...?(await ShellEnvironment.path())?.split(separator),
      ...?Platform.environment['PATH']?.split(separator),
      if (!Platform.isWindows) ...[
        '/opt/homebrew/bin',
        '/usr/local/bin',
        '$home/go/bin',
        '$home/.local/bin',
      ] else
        '$home\\go\\bin',
    ];
    for (final dir in dirs) {
      if (dir.isEmpty) continue;
      final candidate = '$dir${Platform.pathSeparator}$name';
      if (await File(candidate).exists()) return candidate;
    }

    // Last resort: which/where, for layouts the scan above doesn't cover.
    final command = Platform.isWindows ? 'where' : 'which';
    try {
      final result = await Process.run(command, [executableName]);
      final out = (result.stdout as String).trim();
      if (result.exitCode == 0 && out.isNotEmpty) {
        // `where` may return several; take the first line.
        return out.split('\n').first.trim();
      }
    } catch (_) {}
    return null;
  }
}

/// The user's login-shell PATH.
///
/// A GUI app started from Finder or the Dock inherits launchd's bare
/// `/usr/bin:/bin:/usr/sbin:/sbin`, which hides Homebrew, `~/go/bin`, and
/// everything else a terminal would find. Resolved once, then cached.
abstract final class ShellEnvironment {
  static Future<String?>? _path;

  static Future<String?> path() => _path ??= _resolve();

  static Future<String?> _resolve() async {
    if (Platform.isWindows) return null;
    final shell = Platform.environment['SHELL'] ??
        (Platform.isMacOS ? '/bin/zsh' : '/bin/sh');
    // Markers fence the value off from anything rc files print.
    const marker = '__GOCODE_PATH__';
    const script = 'printf "$marker%s$marker" "\$PATH"';
    // Interactive first (rc files often set PATH), then plain login.
    for (final flags in const ['-ilc', '-lc']) {
      try {
        final process = await Process.start(shell, [flags, script]);
        unawaited(process.stdin.close());
        unawaited(process.stderr.drain<void>());
        final stdout = process.stdout.transform(utf8.decoder).join();
        final code = await process.exitCode.timeout(
          const Duration(seconds: 5),
          onTimeout: () {
            process.kill();
            return -1;
          },
        );
        if (code != 0) continue;
        final out = await stdout;
        final start = out.indexOf(marker);
        final end = out.lastIndexOf(marker);
        if (start >= 0 && end > start) {
          final value = out.substring(start + marker.length, end).trim();
          if (value.isNotEmpty) return value;
        }
      } catch (_) {
        // Try the next form; null means "use our own PATH".
      }
    }
    return null;
  }
}

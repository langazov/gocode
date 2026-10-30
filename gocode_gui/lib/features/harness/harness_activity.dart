import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/harness_models.dart';
import '../../core/api/models.dart' show ApiEvent;
import '../../core/connection/controller.dart';
import 'harness_state.dart';

/// What one harness session (a rollout, or an editor, selector or reflector)
/// is doing right now — the GUI's counterpart of the TUI's per-row activity
/// (harnessRowActivity). Folded from the session events the server relays
/// for every Harness² stack (cmd/gocode harness_exec.go's Relay), so it
/// needs no request of its own.
class HarnessSessionActivity {
  const HarnessSessionActivity({
    required this.lastEventAt,
    this.tool,
    this.toolInput = '',
    this.tools = 0,
    this.steps = 0,
    this.failedTools = 0,
  });

  /// When the session last did anything. A session that stays quiet for the
  /// run's idle limit (15m by default) is stopped as stalled; the row warns
  /// well before that.
  final DateTime lastEventAt;

  /// The tool call in flight, or null between calls.
  final String? tool;

  /// A one-line summary of [tool]'s input: the command, path or pattern.
  final String toolInput;
  final int tools;
  final int steps;
  final int failedTools;

  HarnessSessionActivity copyWith({
    required DateTime lastEventAt,
    Object? tool = _unset,
    String? toolInput,
    int? tools,
    int? steps,
    int? failedTools,
  }) => HarnessSessionActivity(
    lastEventAt: lastEventAt,
    tool: identical(tool, _unset) ? this.tool : tool as String?,
    toolInput: toolInput ?? this.toolInput,
    tools: tools ?? this.tools,
    steps: steps ?? this.steps,
    failedTools: failedTools ?? this.failedTools,
  );
}

const _unset = Object();

/// Activity for the sessions of running harness rows, keyed by session ID.
/// Only those sessions are kept: a run's sessions are forgotten once it
/// stops, so the map never outgrows what is on screen.
class HarnessActivityNotifier
    extends Notifier<Map<String, HarnessSessionActivity>> {
  StreamSubscription<ApiEvent>? _events;

  @override
  Map<String, HarnessSessionActivity> build() {
    ref.onDispose(() => _events?.cancel());
    final connection = ref.watch(connectionControllerProvider);
    _events = connection?.events.listen(_onEvent);
    // Drop sessions whose row is no longer running.
    ref.listen(_watchedSessionsProvider, (_, watched) {
      if (state.keys.any((id) => !watched.contains(id))) {
        state = {
          for (final e in state.entries)
            if (watched.contains(e.key)) e.key: e.value,
        };
      }
    });
    return const {};
  }

  void _onEvent(ApiEvent event) {
    final id = event.sessionID;
    if (id == null || !event.type.startsWith('session.next.')) return;
    if (!ref.read(_watchedSessionsProvider).contains(id)) return;
    final now = DateTime.now();
    final current = state[id] ?? HarnessSessionActivity(lastEventAt: now);
    final next = switch (event.type) {
      'session.next.step.started' => current.copyWith(
        lastEventAt: now,
        steps: current.steps + 1,
      ),
      'session.next.tool.called' => current.copyWith(
        lastEventAt: now,
        tool: event.data['tool'] as String?,
        toolInput: harnessToolInput(event.data['input']),
        tools: current.tools + 1,
      ),
      'session.next.tool.success' => current.copyWith(
        lastEventAt: now,
        tool: null,
        toolInput: '',
      ),
      'session.next.tool.failed' => current.copyWith(
        lastEventAt: now,
        tool: null,
        toolInput: '',
        failedTools: current.failedTools + 1,
      ),
      _ => current.copyWith(lastEventAt: now),
    };
    state = {...state, id: next};
  }
}

final harnessActivityProvider =
    NotifierProvider<
      HarnessActivityNotifier,
      Map<String, HarnessSessionActivity>
    >(HarnessActivityNotifier.new);

/// The session IDs of every row in a running run.
final _watchedSessionsProvider = Provider<Set<String>>((ref) {
  final runs = ref.watch(harnessRunsProvider.select((s) => s.runs));
  return {
    for (final run in runs)
      if (run.isRunning)
        for (final row in run.rows) ...[
          if (row.sessionID.isNotEmpty) row.sessionID,
          if (row.editSessionID.isNotEmpty) row.editSessionID,
        ],
  };
});

/// A tool input as one short line: the field a person would look at first
/// (the TUI's toolInputSummary).
String harnessToolInput(Object? input) {
  if (input is! Map) return '';
  for (final key in const [
    'command',
    'filePath',
    'path',
    'pattern',
    'url',
    'query',
    'description',
    'prompt',
  ]) {
    final value = input[key];
    if (value is String && value.trim().isNotEmpty) {
      return value.trim().replaceAll(RegExp(r'\s+'), ' ');
    }
  }
  return '';
}

/// A duration as the run view shows it: `42s`, `3m 05s`, `1h 12m`.
String harnessDuration(Duration d) {
  if (d.isNegative) d = Duration.zero;
  final h = d.inHours, m = d.inMinutes % 60, s = d.inSeconds % 60;
  if (h > 0) return '${h}h ${m.toString().padLeft(2, '0')}m';
  if (m > 0) return '${m}m ${s.toString().padLeft(2, '0')}s';
  return '${s}s';
}

/// A run's event log, followed live: read once, then — while the run is
/// going — re-read every [harnessLogInterval] from the last line held
/// (`?after=N`), the way `gocode harness show --follow` tails events.jsonl.
class HarnessLogNotifier extends AsyncNotifier<List<HarnessEvent>> {
  HarnessLogNotifier(this.runID);

  final String runID;
  Timer? _poll;
  bool _fetching = false;

  @override
  Future<List<HarnessEvent>> build() async {
    ref.onDispose(() => _poll?.cancel());
    final client = ref.watch(apiClientProvider);
    if (client == null) throw StateError('not connected to a gocode server');
    final running = ref.watch(
      harnessRunProvider(runID).select((r) => r?.isRunning ?? false),
    );
    final events = await client.harnessLog(runID);
    _poll?.cancel();
    if (running) {
      _poll = Timer.periodic(
        ref.read(harnessLogIntervalProvider),
        (_) => _more(),
      );
    }
    return events;
  }

  Future<void> _more() async {
    final client = ref.read(apiClientProvider);
    final held = state.value;
    if (client == null || held == null || _fetching) return;
    _fetching = true;
    try {
      final more = await client.harnessLog(runID, after: held.length);
      if (more.isNotEmpty && ref.mounted) {
        state = AsyncData([...held, ...more]);
      }
    } catch (_) {
      // The next tick tries again; the list already shown stays.
    } finally {
      _fetching = false;
    }
  }
}

final harnessLogProvider = AsyncNotifierProvider.autoDispose
    .family<HarnessLogNotifier, List<HarnessEvent>, String>(
      HarnessLogNotifier.new,
    );

/// How often a followed log is re-read. Overridden in tests.
final harnessLogIntervalProvider = Provider<Duration>(
  (_) => const Duration(seconds: 2),
);

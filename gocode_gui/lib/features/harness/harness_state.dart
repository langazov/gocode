import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/client.dart';
import '../../core/api/harness_models.dart';
import '../../core/api/models.dart' show ApiEvent;
import '../../core/connection/controller.dart';

/// Harness² state: the runs list folded from live `harness.*` events and
/// reconciled from `GET /api/harness/run` (documentation/HARNESS2_GUI.md
/// §4.3). The event stream is lossy by design, so every reconnect — and a
/// slow poll while anything runs — re-reads the server's ground truth.
class HarnessRunsState {
  const HarnessRunsState({
    this.unavailable = false,
    this.loading = false,
    this.runs = const [],
    this.error,
    this.lastEnded,
  });

  /// ACP mode (or disconnected): no HTTP API, so nothing to show.
  final bool unavailable;
  final bool loading;
  final List<HarnessRun> runs;
  final String? error;

  /// The run that just left `running`, consumed once by the shell for the
  /// run-end SnackBar. Never repeats on rebuild.
  final HarnessRunEnded? lastEnded;

  HarnessRunsState copyWith({
    bool? unavailable,
    bool? loading,
    List<HarnessRun>? runs,
    Object? error = _unset,
    Object? lastEnded = _unset,
  }) => HarnessRunsState(
    unavailable: unavailable ?? this.unavailable,
    loading: loading ?? this.loading,
    runs: runs ?? this.runs,
    error: identical(error, _unset) ? this.error : error as String?,
    lastEnded: identical(lastEnded, _unset)
        ? this.lastEnded
        : lastEnded as HarnessRunEnded?,
  );
}

const _unset = Object();

/// One run's end, as announced to the user: the run plus the message line.
class HarnessRunEnded {
  const HarnessRunEnded(this.run, this.noticeID);

  final HarnessRun run;

  /// One announcement per run end: the run's id and final status.
  final String noticeID;
}

class HarnessRunsNotifier extends Notifier<HarnessRunsState> {
  StreamSubscription<ApiEvent>? _events;
  StreamSubscription<void>? _reconnect;
  Timer? _poll;
  bool _loading = false;

  /// While any run is going, reconcile this often (the backstop for a
  /// dropped event; the stream itself carries every update). Read through
  /// [harnessPollIntervalProvider] so tests can speed it up.
  static const pollInterval = Duration(seconds: 10);

  @override
  HarnessRunsState build() {
    ref.onDispose(() {
      _events?.cancel();
      _reconnect?.cancel();
      _poll?.cancel();
    });
    final connection = ref.watch(connectionControllerProvider);
    final client = ref.watch(apiClientProvider);
    // ACP mode (or disconnected): no HTTP API, so nothing to show. The
    // controller can lag the client (it exists during "connecting"), so
    // the client is the gate; events simply flow once the controller is
    // there — and every reconnect re-reads the list anyway.
    if (client == null) {
      return const HarnessRunsState(unavailable: true);
    }
    if (connection != null) {
      _events = connection.events.listen(_onEvent);
      _reconnect = connection.reconnectSignal.listen((_) => reconcile());
    }
    Future.microtask(reconcile);
    return const HarnessRunsState(loading: true);
  }

  GocodeClient? get _client => ref.read(apiClientProvider);

  void _onEvent(ApiEvent event) {
    if (!event.type.startsWith('harness.')) return;
    // Every harness.* event carries {run, event} with the run's whole
    // state, so folding is replace-by-id — a dropped event loses nothing.
    final run = event.data['run'];
    if (run is! Map<String, dynamic>) return;
    _fold(HarnessRun.fromJson(run));
  }

  /// The aggregator's rule (the TUI's mergeHarnessRuns): replace by id,
  /// insert new runs at the head, and announce a run leaving `running`.
  void _fold(HarnessRun run) {
    final runs = [...state.runs];
    final i = runs.indexWhere((r) => r.id == run.id);
    HarnessRun? ended;
    if (i >= 0) {
      if (runs[i].isRunning && !run.isRunning) ended = run;
      runs[i] = run;
    } else {
      if (run.isRunning) runs.insert(0, run);
      // A first sight of a finished run arrives on reconcile instead; an
      // insert here would drop it from the next full refresh's ordering.
    }
    state = state.copyWith(runs: runs);
    if (ended != null) _announceEnd(ended);
    _syncPoll();
  }

  void _announceEnd(HarnessRun run) {
    state = state.copyWith(
      lastEnded: HarnessRunEnded(run, '${run.id}:${run.status}'),
    );
  }

  /// Re-reads the server's list: the initial load, every reconnect, and the
  /// poll while a run is going.
  Future<void> reconcile() async {
    final client = _client;
    if (client == null || _loading) return;
    _loading = true;
    try {
      final runs = await client.harnessRuns();
      if (!ref.mounted) return;
      // Which runs left `running` since our last state?
      final before = {
        for (final r in state.runs)
          if (r.isRunning) r.id: r,
      };
      final ended = <HarnessRun>[];
      for (final run in runs) {
        final was = before[run.id];
        if (was != null && !run.isRunning) ended.add(run);
      }
      state = state.copyWith(
        runs: runs,
        loading: false,
        error: null,
        lastEnded: ended.isEmpty
            ? state.lastEnded
            : HarnessRunEnded(
                ended.first,
                '${ended.first.id}:${ended.first.status}',
              ),
      );
    } catch (e) {
      if (ref.mounted) {
        state = state.copyWith(
          loading: false,
          error: e is ApiException ? e.message : '$e',
        );
      }
    } finally {
      _loading = false;
    }
    _syncPoll();
  }

  /// Polls only while a run is going — the same belt-and-braces
  /// SessionActivityNotifier uses for busy sessions.
  void _syncPoll() {
    final running = state.runs.any((r) => r.isRunning);
    if (running && _poll == null) {
      _poll = Timer.periodic(
        ref.read(harnessPollIntervalProvider),
        (_) => reconcile(),
      );
    } else if (!running && _poll != null) {
      _poll!.cancel();
      _poll = null;
    }
  }

  // ------------------------------------------------------------- mutations

  /// Starts a run for real. A 409 (already going, here or in another
  /// process) is returned to the caller as an [ApiException] so the start
  /// flow can offer to open it; other failures too.
  Future<HarnessRun> start(HarnessStartRequest req) async {
    final client = _client;
    if (client == null) {
      throw StateError('not connected to a gocode server');
    }
    final run = await client.startHarness(req);
    _fold(run);
    return run;
  }

  /// Plans a run (dryRun) without spending anything.
  Future<HarnessRun> plan(HarnessStartRequest req) async {
    final client = _client;
    if (client == null) {
      throw StateError('not connected to a gocode server');
    }
    return client.startHarness(
      HarnessStartRequest(
        prompt: req.prompt,
        id: req.id,
        domain: req.domain,
        workspace: req.workspace,
        k: req.k,
        mode: req.mode,
        model: req.model,
        improverModel: req.improverModel,
        library: req.library,
        crossDomain: req.crossDomain,
        dryRun: true,
      ),
    );
  }

  Future<void> resume(String id) async {
    await _client?.resumeHarness(id);
    await reconcile();
  }

  Future<void> cancel(String id) async {
    await _client?.cancelHarness(id);
    await reconcile();
  }

  /// Deletes a stopped run. Never patches local state optimistically: the
  /// refetch after is the truth (the TUI's rule).
  Future<void> delete(String id) async {
    await _client?.deleteHarness(id);
    final runs = state.runs.where((r) => r.id != id).toList();
    state = state.copyWith(runs: runs);
    await reconcile();
  }

  /// Installs (or, with [dryRun], plans installing) a run's adopted
  /// harness. `to` is project, library or both; [dir] installs into that
  /// directory instead of the server's working directory (S1).
  Future<HarnessInstall> install(
    String id, {
    required String to,
    bool force = false,
    bool dryRun = false,
    String? dir,
  }) async {
    final client = _client;
    if (client == null) {
      throw StateError('not connected to a gocode server');
    }
    return client.installHarness(
      id,
      to: to,
      force: force,
      dryRun: dryRun,
      dir: dir,
    );
  }

  Future<void> forgetEntry(String domain, String task) async {
    await _client?.forgetHarnessEntry(domain, task);
    ref.invalidate(harnessLibraryProvider);
  }

  Future<int> syncLibrary() async {
    final client = _client;
    if (client == null) {
      throw StateError('not connected to a gocode server');
    }
    final uploaded = await client.syncHarnessLibrary();
    ref.invalidate(harnessLibraryProvider);
    return uploaded;
  }
}

final harnessRunsProvider =
    NotifierProvider<HarnessRunsNotifier, HarnessRunsState>(
      HarnessRunsNotifier.new,
    );

/// One run out of the runs list — no second fetch, no second subscription.
final harnessRunProvider = Provider.family<HarnessRun?, String>((ref, id) {
  final runs = ref.watch(harnessRunsProvider.select((s) => s.runs));
  for (final run in runs) {
    if (run.id == id) return run;
  }
  return null;
});

/// The label of a stage currently open, so the notifier can invalidate it.
/// Watching the record keeps the provider alive while the sheet is open.
typedef HarnessStageRef = ({String id, String label});

final harnessStageProvider = FutureProvider.autoDispose
    .family<HarnessStage, HarnessStageRef>((ref, ref_) async {
      final client = ref.watch(apiClientProvider);
      if (client == null) {
        throw StateError('not connected to a gocode server');
      }
      // Re-read when the run's rows change, so a settling rollout refreshes
      // the open stage.
      ref.listen(
        harnessRunsProvider.select(
          (s) => s.runs.where((r) => r.id == ref_.id).map((r) => r.rows),
        ),
        (_, _) => ref.invalidateSelf(),
      );
      return client.harnessStage(ref_.id, ref_.label);
    });

/// The experience library. Invalidated after forget/sync and when a run
/// stores a lesson.
final harnessLibraryProvider = FutureProvider.autoDispose<HarnessLibrary>((
  ref,
) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) {
    throw StateError('not connected to a gocode server');
  }
  // A reflected event means a lesson may have been stored.
  ref.listen(harnessRunsProvider, (prev, next) {
    final before = _reflectedLessons(prev?.runs ?? const []);
    final now = _reflectedLessons(next.runs);
    if (before != now) ref.invalidateSelf();
  });
  return client.harnessLibrary();
});

String _reflectedLessons(List<HarnessRun> runs) => [
  for (final r in runs)
    if (r.lesson.isNotEmpty) '${r.id}:${r.lesson}',
].join('|');

/// One library entry with its full tree.
typedef HarnessEntryRef = ({String domain, String task});

final harnessEntryProvider = FutureProvider.autoDispose
    .family<HarnessEntry, HarnessEntryRef>((ref, ref_) async {
      final client = ref.watch(apiClientProvider);
      if (client == null) {
        throw StateError('not connected to a gocode server');
      }
      return client.harnessEntry(ref_.domain, ref_.task);
    });

/// The offered budgets, cached for the connection's life.
final harnessBudgetsProvider = FutureProvider<List<HarnessBudget>>((ref) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) return const <HarnessBudget>[];
  return client.harnessBudgets();
});

/// The newest running run, for the sidebar badge and the session header.
final activeHarnessRunProvider = Provider<HarnessRun?>((ref) {
  final runs = ref.watch(harnessRunsProvider.select((s) => s.runs));
  for (final run in runs) {
    if (run.isRunning) return run;
  }
  return null;
});

/// While any run is going, reconcile this often (the notifier reads this).
/// Overridden in tests to make the backstop observable.
final harnessPollIntervalProvider = Provider<Duration>(
  (_) => HarnessRunsNotifier.pollInterval,
);

/// Whether the harness surface exists at all (Server and Remote modes only;
/// ACP has no HTTP API).
final harnessAvailableProvider = Provider<bool>(
  (ref) => ref.watch(apiClientProvider) != null,
);

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import 'controller.dart';

/// How long a "just finished" flash stays on before settling to plain idle.
const _finishFlashDuration = Duration(milliseconds: 1600);

/// A session's live run/tool status, as seen from anywhere in the app — not
/// just its own open screen — so the sidebar can show accurate, real-time
/// status for sessions that aren't open.
class SessionActivity {
  const SessionActivity({
    this.busy = false,
    this.runningTool,
    this.justFinished = false,
  });

  static const idle = SessionActivity();

  /// A run (session.next.run.started/ended) is in progress: the model is
  /// working, whether that's generating text or waiting on a tool.
  final bool busy;

  /// The name of a tool call that hasn't settled yet (session.next.tool.called
  /// with no matching success/failed since), or null when nothing's running.
  final String? runningTool;

  /// True for a brief window right after [busy] flips to false, so the UI
  /// can flash an unmistakable "done" instead of quietly going idle.
  final bool justFinished;

  SessionActivity copyWith({
    bool? busy,
    Object? runningTool = _unset,
    bool? justFinished,
  }) => SessionActivity(
    busy: busy ?? this.busy,
    runningTool: identical(runningTool, _unset)
        ? this.runningTool
        : runningTool as String?,
    justFinished: justFinished ?? this.justFinished,
  );
}

const _unset = Object();

/// Tracks [SessionActivity] for every session with events flowing through
/// the merged event bus, keyed by session ID. Mirrors [AsksNotifier]'s
/// build-watches-the-connection-and-resubscribes shape.
class SessionActivityNotifier extends Notifier<Map<String, SessionActivity>> {
  StreamSubscription<ApiEvent>? _sub;
  final _finishTimers = <String, Timer>{};

  @override
  Map<String, SessionActivity> build() {
    ref.onDispose(() {
      _sub?.cancel();
      for (final timer in _finishTimers.values) {
        timer.cancel();
      }
      _finishTimers.clear();
    });

    final connection = ref.watch(connectionControllerProvider);
    _sub = connection?.events.listen(_onEvent);
    return const {};
  }

  void _onEvent(ApiEvent event) {
    final sessionID = event.sessionID;
    if (sessionID == null) return;
    switch (event.type) {
      case 'session.next.run.started':
        _finishTimers.remove(sessionID)?.cancel();
        _update(sessionID, (a) => a.copyWith(busy: true, justFinished: false));
      case 'session.next.run.ended':
      case 'session.next.run.failed':
        _update(
          sessionID,
          (a) => a.copyWith(busy: false, justFinished: true, runningTool: null),
        );
        _finishTimers.remove(sessionID)?.cancel();
        _finishTimers[sessionID] = Timer(_finishFlashDuration, () {
          _finishTimers.remove(sessionID);
          _update(sessionID, (a) => a.copyWith(justFinished: false));
        });
      case 'session.next.tool.called':
        final tool = event.data['tool'] as String?;
        _update(sessionID, (a) => a.copyWith(runningTool: tool));
      case 'session.next.tool.success':
      case 'session.next.tool.failed':
        _update(sessionID, (a) => a.copyWith(runningTool: null));
    }
  }

  void _update(
    String sessionID,
    SessionActivity Function(SessionActivity current) apply,
  ) {
    final current = state[sessionID] ?? SessionActivity.idle;
    state = {...state, sessionID: apply(current)};
  }
}

final sessionActivityProvider =
    NotifierProvider<SessionActivityNotifier, Map<String, SessionActivity>>(
      SessionActivityNotifier.new,
    );

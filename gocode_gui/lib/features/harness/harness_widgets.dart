import 'dart:async';

import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/glass.dart' show StatusPill;
import 'harness_activity.dart';

/// Keys tests reach the harness surface's controls by.
abstract final class HarnessKeys {
  static const newRun = ValueKey('harness-new-run');
  static const library = ValueKey('harness-library');
  static const refresh = ValueKey('harness-refresh');
  static const cancel = ValueKey('harness-cancel');
  static const delete = ValueKey('harness-delete');
  static const install = ValueKey('harness-install');
  static const publish = ValueKey('harness-publish');
  static const start = ValueKey('harness-start');
  static const openRun = ValueKey('harness-open-run');
  static const projectFilter = ValueKey('harness-project-filter');
  static const taskField = ValueKey('harness-task-field');
  static const budgetList = ValueKey('harness-budget-list');
  static const confirmInstall = ValueKey('harness-confirm-install');
  static const eventLog = ValueKey('harness-event-log');
  static const resume = ValueKey('harness-resume');
  static const confirmResume = ValueKey('harness-confirm-resume');
}

/// The display phase order, mirroring the TUI's harnessPhaseLabel.
const harnessPhaseLabels = <String, String>{
  'base': 'Base rollouts',
  'probe': 'Propose and probe',
  'compose': 'Compose',
  'select': 'Select',
  'reflect': 'Reflect',
  'other': 'Other',
};

String harnessPhaseLabel(String phase) => harnessPhaseLabels[phase] ?? 'Other';

/// A run's status chip: running in the accent, ok in green, dim for
/// partial/cancelled, red for failures.
class HarnessStatusChip extends StatelessWidget {
  const HarnessStatusChip({super.key, required this.status});

  final String status;

  @override
  Widget build(BuildContext context) {
    final (label, color) = switch (status) {
      'running' => ('running', GC.accent),
      'planned' => ('planned', GC.textDim),
      'ok' => ('ok', GC.ok),
      'partial' => ('partial', GC.textDim),
      'cancelled' => ('cancelled', GC.textDim),
      'interrupted' => ('interrupted', GC.textDim),
      _ => (status.isEmpty ? 'unknown' : status, GC.down),
    };
    return StatusPill(label: label, color: color);
  }
}

/// A row's state glyph, with the state name as its semantic label.
class HarnessRowGlyph extends StatelessWidget {
  const HarnessRowGlyph({super.key, required this.state});

  final String state;

  @override
  Widget build(BuildContext context) {
    final (glyph, color) = switch (state) {
      'editing' || 'rolling' => ('◐', GC.accent),
      'edited' => ('~', GC.textDim),
      'ok' => ('✓', GC.ok),
      'failed' => ('✗', GC.down),
      _ => ('·', GC.textFaint),
    };
    final animate =
        (state == 'editing' || state == 'rolling') &&
        !MediaQuery.maybeDisableAnimationsOf(context)!;
    if (!animate) {
      return SizedBox(
        width: 16,
        child: Semantics(
          label: state,
          child: Text(
            glyph,
            textAlign: TextAlign.center,
            style: TextStyle(fontFamily: GC.mono, fontSize: 13, color: color),
          ),
        ),
      );
    }
    return _SpinningGlyph(glyph: glyph, color: color, label: state);
  }
}

/// The working glyph, a slow half-turn. Honours disableAnimations via
/// [HarnessRowGlyph] (which falls back to the static glyph).
class _SpinningGlyph extends StatefulWidget {
  const _SpinningGlyph({
    required this.glyph,
    required this.color,
    required this.label,
  });

  final String glyph;
  final Color color;
  final String label;

  @override
  State<_SpinningGlyph> createState() => _SpinningGlyphState();
}

class _SpinningGlyphState extends State<_SpinningGlyph>
    with SingleTickerProviderStateMixin {
  late final _controller = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1600),
  )..repeat();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: 16,
      child: Semantics(
        label: widget.label,
        child: RotationTransition(
          turns: _controller,
          child: Text(
            widget.glyph,
            textAlign: TextAlign.center,
            style: TextStyle(
              fontFamily: GC.mono,
              fontSize: 13,
              color: widget.color,
            ),
          ),
        ),
      ),
    );
  }
}

/// A phase section header in the run view; [active] highlights the phase
/// the run is in.
class HarnessPhaseHeader extends StatelessWidget {
  const HarnessPhaseHeader({
    super.key,
    required this.label,
    this.active = false,
  });

  final String label;
  final bool active;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(2, 18, 2, 8),
      child: Row(
        children: [
          Container(
            width: 6,
            height: 6,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: active ? GC.accent : GC.textFaint,
            ),
          ),
          const SizedBox(width: 8),
          Text(
            label.toUpperCase(),
            style: GC.caption.copyWith(
              color: active ? GC.accentText : GC.textFaint,
            ),
          ),
        ],
      ),
    );
  }
}

/// Cost in monospace, dim when zero.
class HarnessCost extends StatelessWidget {
  const HarnessCost(this.value, {super.key});

  final double value;

  @override
  Widget build(BuildContext context) => Text(
    harnessMoney(value),
    style: GC.code.copyWith(fontSize: 11.5, color: GC.textDim),
  );
}

/// The +/~/− file list of a harness diff (the TUI's stage lines).
class HarnessFileList extends StatelessWidget {
  const HarnessFileList({super.key, required this.files});

  final HarnessFiles files;

  @override
  Widget build(BuildContext context) {
    if (files.unchanged) {
      return const Padding(
        padding: EdgeInsets.symmetric(vertical: 4),
        child: Text('Same as base', style: GC.reading),
      );
    }
    final rows = <(String, String)>[
      for (final rel in files.added) ('+', rel),
      for (final rel in files.modified) ('~', rel),
      for (final rel in files.removed) ('-', rel),
    ];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final (sign, rel) in rows)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 1),
            child: Row(
              children: [
                SizedBox(
                  width: 16,
                  child: Text(
                    sign,
                    style: GC.code.copyWith(
                      color: switch (sign) {
                        '+' => GC.ok,
                        '-' => GC.downText,
                        _ => GC.textDim,
                      },
                    ),
                  ),
                ),
                Expanded(
                  child: Text(rel, style: GC.code.copyWith(fontSize: 12)),
                ),
              ],
            ),
          ),
        const SizedBox(height: 4),
        Text(
          '${files.alwaysOnCharsDelta >= 0 ? '+' : ''}'
          '${files.alwaysOnCharsDelta} always-on chars',
          style: GC.code.copyWith(fontSize: 11, color: GC.textFaint),
        ),
      ],
    );
  }
}

/// Time since [since], ticking each second while [live]; a fixed duration
/// once it is not (a finished run shows how long it took up to [until]).
class HarnessElapsed extends StatefulWidget {
  const HarnessElapsed({
    super.key,
    required this.since,
    this.until,
    this.live = true,
    this.style,
  });

  final DateTime since;
  final DateTime? until;
  final bool live;
  final TextStyle? style;

  @override
  State<HarnessElapsed> createState() => _HarnessElapsedState();
}

class _HarnessElapsedState extends State<HarnessElapsed> {
  Timer? _tick;

  @override
  void initState() {
    super.initState();
    _sync();
  }

  @override
  void didUpdateWidget(HarnessElapsed old) {
    super.didUpdateWidget(old);
    _sync();
  }

  void _sync() {
    if (widget.live && _tick == null) {
      _tick = Timer.periodic(const Duration(seconds: 1), (_) {
        if (mounted) setState(() {});
      });
    } else if (!widget.live) {
      _tick?.cancel();
      _tick = null;
    }
  }

  @override
  void dispose() {
    _tick?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final end = widget.live ? DateTime.now() : (widget.until ?? DateTime.now());
    return Text(
      harnessDuration(end.difference(widget.since)),
      style:
          widget.style ?? GC.code.copyWith(fontSize: 11.5, color: GC.textDim),
    );
  }
}

/// After this long without an event a working row is marked quiet; the
/// run stops a session at its idle limit (15m by default).
const harnessQuietAfter = Duration(minutes: 2);

/// A working row's live line: the tool in flight with its input, counts,
/// and how long since the session last did anything — amber once quiet.
class HarnessActivityLine extends StatefulWidget {
  const HarnessActivityLine({super.key, required this.activity, this.role});

  final HarnessSessionActivity activity;

  /// Which session this is when the row has two (rollout / editor).
  final String? role;

  @override
  State<HarnessActivityLine> createState() => _HarnessActivityLineState();
}

class _HarnessActivityLineState extends State<HarnessActivityLine> {
  late final Timer _tick = Timer.periodic(
    const Duration(seconds: 1),
    (_) => mounted ? setState(() {}) : null,
  );

  @override
  void dispose() {
    _tick.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final a = widget.activity;
    final idle = DateTime.now().difference(a.lastEventAt);
    final quiet = idle >= harnessQuietAfter;
    final doing = a.tool == null
        ? 'thinking'
        : [a.tool!, if (a.toolInput.isNotEmpty) a.toolInput].join(' · ');
    final counts = [
      '${a.steps} steps',
      '${a.tools} tools',
      if (a.failedTools > 0) '${a.failedTools} refused or failed',
    ].join(' · ');
    final base = GC.code.copyWith(fontSize: 11, color: GC.textFaint);
    return Padding(
      padding: const EdgeInsets.only(top: 3),
      child: Text.rich(
        TextSpan(
          children: [
            if (widget.role != null)
              TextSpan(text: '${widget.role} ', style: base),
            TextSpan(
              text: doing,
              style: base.copyWith(color: GC.textDim),
            ),
            TextSpan(text: '  $counts  ', style: base),
            TextSpan(
              text: quiet
                  ? 'quiet ${harnessDuration(idle)}'
                  : '${harnessDuration(idle)} ago',
              style: base.copyWith(color: quiet ? GC.warn : GC.textFaint),
            ),
          ],
        ),
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
    );
  }
}

/// Opens a rollout or improver session to watch it live.
class HarnessWatchButton extends StatelessWidget {
  const HarnessWatchButton({
    super.key,
    required this.sessionID,
    required this.tooltip,
    required this.icon,
  });

  final String sessionID;
  final String tooltip;
  final IconData icon;

  @override
  Widget build(BuildContext context) {
    return IconButton(
      tooltip: tooltip,
      visualDensity: VisualDensity.compact,
      iconSize: 17,
      color: GC.textDim,
      icon: Icon(icon),
      onPressed: () => GoRouter.of(context).push('/session/$sessionID'),
    );
  }
}

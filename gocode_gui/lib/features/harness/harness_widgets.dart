import 'package:flutter/material.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/glass.dart' show StatusPill;

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

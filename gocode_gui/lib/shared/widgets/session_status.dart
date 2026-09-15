import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/connection/session_activity.dart';
import 'glass.dart';

/// A live status dot: a soft expanding "ping" ring loops while [busy], and
/// the instant [justFinished] turns true it bursts into one bright green
/// flash — so a run's end reads immediately, not just as the pulse quietly
/// stopping. Idle otherwise draws nothing but a faint static dot.
///
/// Animates only while there's something to animate (busy, or the one-shot
/// finish flash), so dozens of idle rows in a list cost nothing.
class LiveDot extends StatefulWidget {
  const LiveDot({
    super.key,
    required this.busy,
    this.justFinished = false,
    this.size = 8,
  });

  final bool busy;
  final bool justFinished;
  final double size;

  @override
  State<LiveDot> createState() => _LiveDotState();
}

class _LiveDotState extends State<LiveDot> with TickerProviderStateMixin {
  late final _pulse = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1400),
  );
  late final _flash = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 750),
  );

  @override
  void initState() {
    super.initState();
    if (widget.busy) _pulse.repeat();
    if (widget.justFinished) _flash.forward(from: 0);
  }

  @override
  void didUpdateWidget(LiveDot old) {
    super.didUpdateWidget(old);
    if (widget.busy && !_pulse.isAnimating) _pulse.repeat();
    if (!widget.busy && _pulse.isAnimating) {
      _pulse
        ..stop()
        ..value = 0;
    }
    if (widget.justFinished && !old.justFinished) _flash.forward(from: 0);
  }

  @override
  void dispose() {
    _pulse.dispose();
    _flash.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: Listenable.merge([_pulse, _flash]),
      builder: (context, _) {
        final flashing = _flash.isAnimating;
        final color = flashing || widget.justFinished
            ? GC.ok
            : widget.busy
            ? GC.accent
            : GC.textFaint.withValues(alpha: 0.55);
        final ringActive = widget.busy || flashing;
        final t = flashing ? _flash.value : _pulse.value;
        return SizedBox.square(
          dimension: widget.size * 3,
          child: Stack(
            alignment: Alignment.center,
            children: [
              if (ringActive)
                Opacity(
                  opacity: (1 - t) * (flashing ? 0.6 : 0.35),
                  child: Transform.scale(
                    scale: 1 + t * (flashing ? 2.2 : 1.6),
                    child: _dot(color, widget.size),
                  ),
                ),
              AnimatedContainer(
                duration: const Duration(milliseconds: 220),
                curve: GC.ease,
                width: widget.size,
                height: widget.size,
                decoration: BoxDecoration(color: color, shape: BoxShape.circle),
              ),
            ],
          ),
        );
      },
    );
  }

  Widget _dot(Color color, double size) => Container(
    width: size,
    height: size,
    decoration: BoxDecoration(color: color, shape: BoxShape.circle),
  );
}

/// A [StatusPill] with a [LiveDot], reading [SessionActivity] for [sessionID]
/// from the global event bus. [busy] is passed in rather than read from the
/// same source because a session's own open screen already tracks it more
/// directly (and instantly) via its own event subscription; this widget
/// layers the running-tool detail and the finish flash on top of that.
class LiveStatusPill extends ConsumerWidget {
  const LiveStatusPill({
    super.key,
    required this.sessionID,
    required this.busy,
  });

  final String sessionID;
  final bool busy;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final activity =
        ref.watch(sessionActivityProvider)[sessionID] ?? SessionActivity.idle;
    final tool = activity.runningTool;
    final label = activity.justFinished
        ? 'done'
        : busy
        ? (tool ?? 'working')
        : 'idle';
    final color = activity.justFinished
        ? GC.ok
        : busy
        ? GC.warn
        : GC.textDim;
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        LiveDot(busy: busy, justFinished: activity.justFinished, size: 7),
        const SizedBox(width: 7),
        AnimatedSwitcher(
          duration: const Duration(milliseconds: 180),
          child: StatusPill(
            key: ValueKey(label),
            label: label,
            color: color,
          ),
        ),
      ],
    );
  }
}

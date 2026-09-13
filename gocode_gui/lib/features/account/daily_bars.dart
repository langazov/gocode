import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../app/theme.dart';
import '../../core/api/account.dart';
import 'account_widgets.dart';

/// Bar color: the accent's pressed step, which (unlike the brighter accent)
/// sits inside the dark-mode lightness band and clears 3:1 on the surface —
/// checked with the dataviz palette validator.
const _barColor = GC.accentPress;
const _barHover = GC.accent;

/// Tokens per day as columns: one series, so no legend (the card's title
/// names it). Hairline gridlines at round values, ≤24px bars with a rounded
/// data end on a shared baseline, a 2px gap, and a tooltip per day.
class DailyBars extends StatelessWidget {
  const DailyBars({super.key, required this.days});

  final List<UsageDay> days;

  static const _axisWidth = 44.0;
  static const _dateHeight = 20.0;

  @override
  Widget build(BuildContext context) {
    final peak = days.fold<int>(0, (m, d) => math.max(m, d.tokens));
    if (peak == 0) {
      return Center(
        child: Text(
          'No usage in this period.',
          style: Theme.of(context).textTheme.bodyMedium,
        ),
      );
    }
    final top = niceCeiling(peak.toDouble());
    const axisText = TextStyle(
      fontFamily: GC.sans,
      fontSize: 11,
      color: GC.textFaint,
      fontFeatures: [FontFeature.tabularFigures()],
    );
    return LayoutBuilder(
      builder: (context, constraints) {
        final plotHeight = constraints.maxHeight - _dateHeight;
        return Stack(
          children: [
            // Gridlines and their labels: 0, half, top.
            for (final fraction in const [0.0, 0.5, 1.0])
              Positioned(
                left: 0,
                right: 0,
                top: plotHeight * (1 - fraction) - 7,
                child: Row(
                  children: [
                    SizedBox(
                      width: _axisWidth - 8,
                      child: Text(
                        compactNumber(top * fraction),
                        textAlign: TextAlign.right,
                        style: axisText,
                      ),
                    ),
                    const SizedBox(width: 8),
                    const Expanded(
                      child: Divider(
                        height: 14,
                        thickness: 1,
                        color: GC.border,
                      ),
                    ),
                  ],
                ),
              ),
            Positioned(
              left: _axisWidth,
              right: 0,
              top: 0,
              height: plotHeight,
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: [
                  for (final day in days)
                    Expanded(
                      child: _Bar(
                        day: day,
                        height: plotHeight * day.tokens / top,
                      ),
                    ),
                ],
              ),
            ),
            // First, middle and last dates; the tooltip carries the rest.
            Positioned(
              left: _axisWidth,
              right: 0,
              bottom: 0,
              height: _dateHeight - 4,
              child: Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  Text(shortDate(days.first.date), style: axisText),
                  if (days.length > 2)
                    Text(
                      shortDate(days[days.length ~/ 2].date),
                      style: axisText,
                    ),
                  Text(shortDate(days.last.date), style: axisText),
                ],
              ),
            ),
          ],
        );
      },
    );
  }
}

class _Bar extends StatefulWidget {
  const _Bar({required this.day, required this.height});

  final UsageDay day;
  final double height;

  @override
  State<_Bar> createState() => _BarState();
}

class _BarState extends State<_Bar> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    final day = widget.day;
    // The whole column is the hit target, not just the (maybe tiny) bar.
    return MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: Tooltip(
        message:
            '${shortDate(day.date)}: ${groupedNumber(day.tokens)} tokens · '
            '${groupedNumber(day.requests)} requests',
        waitDuration: Duration.zero,
        child: Container(
          color: _hovered ? const Color(0x0AFFFFFF) : Colors.transparent,
          alignment: Alignment.bottomCenter,
          // 1px either side: a 2px surface gap between neighbours.
          padding: const EdgeInsets.symmetric(horizontal: 1),
          child: LayoutBuilder(
            builder: (context, constraints) => Container(
              width: math.min(24, constraints.maxWidth),
              // A non-zero day never vanishes into the baseline.
              height: day.tokens > 0 ? math.max(2, widget.height) : 0,
              decoration: BoxDecoration(
                color: _hovered ? _barHover : _barColor,
                borderRadius: const BorderRadius.vertical(
                  top: Radius.circular(4),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// The smallest 1/2/2.5/5 × 10ⁿ at or above [value], for clean axis ticks.
double niceCeiling(double value) {
  if (value <= 0) return 1;
  final magnitude = math
      .pow(10, (math.log(value) / math.ln10).floor())
      .toDouble();
  for (final step in const [1.0, 2.0, 2.5, 5.0, 10.0]) {
    if (step * magnitude >= value) return step * magnitude;
  }
  return 10 * magnitude;
}

/// "Sep 13".
String shortDate(DateTime date) {
  const months = [
    'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', //
    'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
  ];
  return '${months[date.month - 1]} ${date.day}';
}

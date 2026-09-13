import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../app/theme.dart';
import '../../shared/widgets/glass.dart';

/// Opens a gocoder.org page in the browser.
Future<void> openExternal(Uri uri) =>
    launchUrl(uri, mode: LaunchMode.externalApplication);

/// "Jan 2026".
String monthYear(DateTime date) {
  const months = [
    'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', //
    'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
  ];
  return '${months[date.month - 1]} ${date.year}';
}

/// 12,345.
String groupedNumber(num value) {
  final digits = value.round().abs().toString();
  final out = StringBuffer(value < 0 ? '-' : '');
  for (var i = 0; i < digits.length; i++) {
    if (i > 0 && (digits.length - i) % 3 == 0) out.write(',');
    out.write(digits[i]);
  }
  return out.toString();
}

/// 1.2k, 3.4M — for tight spaces; full numbers go in tooltips and tables.
String compactNumber(num value) {
  if (value.abs() >= 1e6) return '${_trim(value / 1e6)}M';
  if (value.abs() >= 1e3) return '${_trim(value / 1e3)}k';
  return value.round().toString();
}

String _trim(num value) {
  final fixed = value.toStringAsFixed(1);
  return fixed.endsWith('.0') ? fixed.substring(0, fixed.length - 2) : fixed;
}

/// A titled card on an account page.
class AccountSection extends StatelessWidget {
  const AccountSection({super.key, required this.title, required this.child});

  final String title;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return GlassSurface(
      blur: false,
      radius: GC.rCard,
      padding: const EdgeInsets.all(20),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(title, style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 12),
          child,
        ],
      ),
    );
  }
}

/// A one-line notice with an icon: a warning, or a quieter note.
class NoticePanel extends StatelessWidget {
  const NoticePanel({
    super.key,
    required this.text,
    this.icon = Icons.info_outline_rounded,
    this.color = GC.textDim,
  });

  final String text;
  final IconData icon;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.08),
        borderRadius: BorderRadius.circular(GC.rInput),
        border: Border.all(color: color.withValues(alpha: 0.35)),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: 2),
            child: Icon(icon, size: 18, color: color),
          ),
          const SizedBox(width: 10),
          Expanded(
            child: Text(text, style: Theme.of(context).textTheme.bodyMedium),
          ),
        ],
      ),
    );
  }
}

/// A label over a big number (the dashboard stat tile).
class StatTile extends StatelessWidget {
  const StatTile({
    super.key,
    required this.label,
    required this.value,
    this.detail,
    this.mono = false,
  });

  final String label;
  final String value;
  final String? detail;
  final bool mono;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return ConstrainedBox(
      constraints: const BoxConstraints(minWidth: 150),
      child: GlassSurface(
        blur: false,
        shadow: false,
        radius: GC.rItem,
        padding: const EdgeInsets.fromLTRB(16, 14, 16, 14),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(label, style: theme.textTheme.bodySmall),
            const SizedBox(height: 6),
            Text(
              value,
              style: TextStyle(
                fontFamily: mono ? GC.mono : GC.display,
                fontSize: mono ? 20 : 26,
                fontWeight: FontWeight.w700,
                height: 1.1,
                color: GC.textHi,
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            if (detail != null) ...[
              const SizedBox(height: 4),
              Text(detail!, style: theme.textTheme.labelSmall),
            ],
          ],
        ),
      ),
    );
  }
}

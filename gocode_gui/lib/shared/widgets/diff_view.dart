import 'package:flutter/material.dart';

import '../../app/theme.dart';

/// A minimal unified-diff model: enough to render, no dependencies.
class DiffLine {
  const DiffLine({
    required this.type,
    required this.text,
    required this.oldLine,
    required this.newLine,
  });

  /// 'context' | 'add' | 'remove' | 'meta'
  final String type;
  final String text;

  /// 1-based, 0 when absent.
  final int oldLine;
  final int newLine;

  bool get isAdd => type == 'add';
  bool get isRemove => type == 'remove';
  bool get isMeta => type == 'meta';
}

/// Parses a unified diff patch string into renderable lines.
///
/// Handles `diff --git`/`+++`/`---`/`@@` headers, `+`/`-`/` ` content lines,
/// and `\ No newline` markers. Tolerant: anything unparsable renders as-is.
List<DiffLine> parseUnifiedDiff(String patch) {
  final out = <DiffLine>[];
  var oldLine = 0;
  var newLine = 0;
  var inHunk = false;

  if (patch.isEmpty) return out;
  for (final raw in patch.split('\n')) {
    if (raw.startsWith('@@')) {
      // "@@ -oldStart[,count] +newStart[,count] @@ …"
      final match = RegExp(r'@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@')
          .firstMatch(raw);
      if (match != null) {
        oldLine = int.parse(match.group(1)!);
        newLine = int.parse(match.group(2)!);
        inHunk = true;
        out.add(DiffLine(type: 'meta', text: raw, oldLine: 0, newLine: 0));
        continue;
      }
    }
    if (!inHunk) {
      // File headers and preamble.
      out.add(DiffLine(type: 'meta', text: raw, oldLine: 0, newLine: 0));
      continue;
    }
    if (raw.startsWith('+')) {
      out.add(
        DiffLine(
          type: 'add',
          text: raw.substring(1),
          oldLine: 0,
          newLine: newLine,
        ),
      );
      newLine++;
    } else if (raw.startsWith('-')) {
      out.add(
        DiffLine(
          type: 'remove',
          text: raw.substring(1),
          oldLine: oldLine,
          newLine: 0,
        ),
      );
      oldLine++;
    } else if (raw.startsWith(' ')) {
      out.add(
        DiffLine(
          type: 'context',
          text: raw.substring(1),
          oldLine: oldLine,
          newLine: newLine,
        ),
      );
      oldLine++;
      newLine++;
    } else if (raw.startsWith('\\')) {
      out.add(DiffLine(type: 'meta', text: raw, oldLine: 0, newLine: 0));
    } else if (raw.trim().isEmpty) {
      out.add(
        DiffLine(type: 'context', text: '', oldLine: oldLine, newLine: newLine),
      );
      oldLine++;
      newLine++;
    } else {
      out.add(DiffLine(type: 'meta', text: raw, oldLine: 0, newLine: 0));
    }
  }
  return out;
}

/// Renders a unified diff: per-line gutter, +/- washes, monospace text.
class DiffView extends StatelessWidget {
  const DiffView({super.key, required this.patch, this.fileName});

  final String patch;
  final String? fileName;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final lines = parseUnifiedDiff(patch);
    final additions = lines.where((l) => l.isAdd).length;
    final removals = lines.where((l) => l.isRemove).length;
    const radius = Radius.circular(GC.rInput);

    return Container(
      decoration: BoxDecoration(
        color: const Color(0x40000000),
        borderRadius: const BorderRadius.all(radius),
        border: Border.all(color: GC.border),
      ),
      clipBehavior: Clip.antiAlias,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (fileName != null)
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              decoration: const BoxDecoration(
                color: Color(0x0AFFFFFF),
                border: Border(bottom: BorderSide(color: GC.border)),
              ),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      fileName!,
                      style: GC.code.copyWith(
                        fontWeight: FontWeight.w600,
                        color: GC.textHi,
                      ),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  Text('+$additions', style: GC.code.copyWith(color: GC.ok)),
                  const SizedBox(width: 6),
                  Text(
                    '−$removals',
                    style: GC.code.copyWith(color: GC.downText),
                  ),
                ],
              ),
            ),
          if (lines.isEmpty)
            Padding(
              padding: const EdgeInsets.all(12),
              child: Text('No changes', style: theme.textTheme.bodySmall),
            )
          else
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 6),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [for (final line in lines) _line(line)],
              ),
            ),
        ],
      ),
    );
  }

  Widget _line(DiffLine line) {
    if (line.isMeta) {
      return Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 2),
        child: Text(
          line.text,
          style: GC.code.copyWith(color: GC.accentText.withValues(alpha: 0.7)),
        ),
      );
    }
    final (background, foreground, sign) = line.isAdd
        ? (GC.ok.withValues(alpha: 0.10), GC.textHi, '+')
        : line.isRemove
        ? (GC.down.withValues(alpha: 0.10), GC.textHi, '-')
        : (Colors.transparent, GC.textBody, ' ');
    final number = line.isRemove ? line.oldLine : line.newLine;
    return Container(
      color: background,
      padding: const EdgeInsets.symmetric(horizontal: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 44,
            child: Text(
              '$number',
              textAlign: TextAlign.right,
              style: GC.code.copyWith(color: GC.textFaint),
            ),
          ),
          SizedBox(
            width: 20,
            child: Text(
              sign,
              textAlign: TextAlign.center,
              style: GC.code.copyWith(
                color: line.isAdd
                    ? GC.ok
                    : line.isRemove
                    ? GC.downText
                    : GC.textFaint,
              ),
            ),
          ),
          Expanded(
            child: Text(
              line.text.isEmpty ? ' ' : line.text,
              style: GC.code.copyWith(color: foreground),
            ),
          ),
        ],
      ),
    );
  }
}

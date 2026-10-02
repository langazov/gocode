// Ported verbatim from goide (app/lib/git/diff_model.dart).

/// Parsing of unified diffs (as produced by `git diff`) into hunks and lines,
/// pairing for side-by-side display, and building single-hunk patches for
/// `git apply` (stage / unstage / discard a hunk).
library;

enum DiffLineKind { context, add, del, meta }

class DiffLine {
  const DiffLine(this.kind, this.text, {this.oldNo, this.newNo});

  final DiffLineKind kind;
  final String text; // without the leading +/-/space
  final int? oldNo;
  final int? newNo;
}

class DiffHunk {
  DiffHunk({
    required this.header,
    required this.oldStart,
    required this.newStart,
    required this.raw,
    required this.lines,
  });

  /// The "@@ -a,b +c,d @@ section" line.
  final String header;
  final int oldStart;
  final int newStart;

  /// The hunk exactly as in the diff (header + body lines), for patches.
  final List<String> raw;
  final List<DiffLine> lines;

  int get additions => lines.where((l) => l.kind == DiffLineKind.add).length;
  int get deletions => lines.where((l) => l.kind == DiffLineKind.del).length;
}

class FileDiff {
  FileDiff({required this.header, required this.hunks, this.binary = false});

  /// Lines before the first hunk ("diff --git", "index", "---", "+++").
  final List<String> header;
  final List<DiffHunk> hunks;
  final bool binary;

  bool get isEmpty => hunks.isEmpty && !binary;
  int get additions => hunks.fold(0, (s, h) => s + h.additions);
  int get deletions => hunks.fold(0, (s, h) => s + h.deletions);

  /// A patch containing only [hunk], applicable with `git apply`.
  String patchFor(DiffHunk hunk) => '${[...header, ...hunk.raw].join('\n')}\n';
}

final _hunkRe = RegExp(r'^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@');

/// Parses a single-file unified diff.
FileDiff parseDiff(String text) {
  final lines = text.split('\n');
  if (lines.isNotEmpty && lines.last.isEmpty) lines.removeLast();
  final header = <String>[];
  final hunks = <DiffHunk>[];
  var binary = false;
  DiffHunk? cur;
  var oldNo = 0, newNo = 0;
  for (final l in lines) {
    final m = _hunkRe.firstMatch(l);
    if (m != null) {
      oldNo = int.parse(m.group(1)!);
      newNo = int.parse(m.group(2)!);
      cur = DiffHunk(
        header: l,
        oldStart: oldNo,
        newStart: newNo,
        raw: [l],
        lines: [],
      );
      hunks.add(cur);
      continue;
    }
    if (cur == null) {
      if (l.startsWith('Binary files') || l.startsWith('GIT binary patch')) {
        binary = true;
      }
      header.add(l);
      continue;
    }
    cur.raw.add(l);
    if (l.startsWith('+')) {
      cur.lines.add(DiffLine(DiffLineKind.add, l.substring(1), newNo: newNo++));
    } else if (l.startsWith('-')) {
      cur.lines.add(DiffLine(DiffLineKind.del, l.substring(1), oldNo: oldNo++));
    } else if (l.startsWith('\\')) {
      cur.lines.add(
        DiffLine(DiffLineKind.meta, l),
      ); // "\ No newline at end of file"
    } else {
      final t = l.isEmpty ? '' : l.substring(1);
      cur.lines.add(
        DiffLine(DiffLineKind.context, t, oldNo: oldNo++, newNo: newNo++),
      );
    }
  }
  return FileDiff(header: header, hunks: hunks, binary: binary);
}

/// One row of a side-by-side view; either side may be empty.
class SplitRow {
  const SplitRow(this.left, this.right);

  final DiffLine? left;
  final DiffLine? right;
}

/// Pairs a hunk's lines for side-by-side display: context lines on both
/// sides, runs of deletions aligned with the following additions.
List<SplitRow> splitRows(DiffHunk hunk) {
  final rows = <SplitRow>[];
  final dels = <DiffLine>[], adds = <DiffLine>[];
  void flush() {
    final n = dels.length > adds.length ? dels.length : adds.length;
    for (var i = 0; i < n; i++) {
      rows.add(
        SplitRow(
          i < dels.length ? dels[i] : null,
          i < adds.length ? adds[i] : null,
        ),
      );
    }
    dels.clear();
    adds.clear();
  }

  for (final l in hunk.lines) {
    switch (l.kind) {
      case DiffLineKind.del:
        if (adds.isNotEmpty) flush();
        dels.add(l);
      case DiffLineKind.add:
        adds.add(l);
      case DiffLineKind.context:
        flush();
        rows.add(SplitRow(l, l));
      case DiffLineKind.meta:
        break;
    }
  }
  flush();
  return rows;
}

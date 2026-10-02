import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/features/git/diff_model.dart';
import 'package:gocode_gui/features/git/graph.dart';

const _diff = '''diff --git a/a.txt b/a.txt
index 1111111..2222222 100644
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
-one
+ONE
 two
 three
@@ -8,3 +8,4 @@ fn section
 eight
 nine
-ten
+TEN
+eleven
\\ No newline at end of file
''';

void main() {
  group('diff parsing', () {
    test('hunks, line numbers, stats', () {
      final d = parseDiff(_diff);
      expect(d.header, hasLength(4));
      expect(d.hunks, hasLength(2));
      expect(d.additions, 3);
      expect(d.deletions, 2);
      final h2 = d.hunks[1];
      expect(h2.oldStart, 8);
      expect(h2.newStart, 8);
      final ten = h2.lines.firstWhere((l) => l.kind == DiffLineKind.del);
      expect(ten.text, 'ten');
      expect(ten.oldNo, 10);
      final eleven = h2.lines.lastWhere((l) => l.kind == DiffLineKind.add);
      expect(eleven.newNo, 11);
      expect(h2.lines.last.kind, DiffLineKind.meta);
    });

    test('single-hunk patch keeps the file header', () {
      final d = parseDiff(_diff);
      final patch = d.patchFor(d.hunks[1]);
      expect(patch, startsWith('diff --git a/a.txt b/a.txt\n'));
      expect(patch, contains('@@ -8,3 +8,4 @@'));
      expect(patch, isNot(contains('@@ -1,3')));
      expect(patch, endsWith('\\ No newline at end of file\n'));
    });

    test('side-by-side pairs deletions with additions', () {
      final rows = splitRows(parseDiff(_diff).hunks[1]);
      // eight, nine (context) | ten/TEN | -/eleven
      expect(rows, hasLength(4));
      expect(rows[2].left?.text, 'ten');
      expect(rows[2].right?.text, 'TEN');
      expect(rows[3].left, isNull);
      expect(rows[3].right?.text, 'eleven');
    });

    test('empty and binary diffs', () {
      expect(parseDiff('').isEmpty, isTrue);
      final bin = parseDiff(
        'diff --git a/x.png b/x.png\nBinary files a/x.png and b/x.png differ\n',
      );
      expect(bin.binary, isTrue);
      expect(bin.isEmpty, isFalse);
    });
  });

  group('graph layout', () {
    test('linear history stays in one lane', () {
      final rows = layoutGraph([
        ('c', ['b']),
        ('b', ['a']),
        ('a', []),
      ]);
      expect(rows.map((r) => r.column), [0, 0, 0]);
      expect(rows.first.lanesIn, [null]); // tip: nothing above
      expect(rows.last.lanesOut, isEmpty);
    });

    test('branch and merge use two lanes and converge', () {
      // m merges f into d; f branched from b.
      //   m
      //   |\
      //   d f
      //   | |
      //   c |
      //   |/
      //   b
      final rows = layoutGraph([
        ('m', ['d', 'f']),
        ('d', ['c']),
        ('f', ['b']),
        ('c', ['b']),
        ('b', []),
      ]);
      expect(rows[0].parentLanes, [0, 1]);
      expect(rows[2].column, 1); // f on the second lane
      expect(rows[3].lanesIn, ['c', 'b']); // f's lane still open at c
      expect(rows[3].lanesOut, ['b']); // …and bends into lane 0 below c
      expect(rows[4].column, 0); // b on the first lane
      expect(rows.map((r) => r.width).reduce((a, b) => a > b ? a : b), 2);
    });

    test('unrelated branch tips get new lanes', () {
      final rows = layoutGraph([
        ('x', ['a']),
        ('y', ['a']),
        ('a', []),
      ]);
      expect(rows[0].column, 0);
      expect(rows[1].column, 1);
      expect(rows[1].parentLanes, [0]); // joins the lane already expecting a
    });
  });
}

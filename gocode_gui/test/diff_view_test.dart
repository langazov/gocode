import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/shared/widgets/diff_view.dart';

void main() {
  group('parseUnifiedDiff', () {
    test('parses hunks and tracks line numbers', () {
      const patch = '''
diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
-func old() {}
+func new() {}
+func added() {}
''';
      final lines = parseUnifiedDiff(patch);

      final hunk = lines.indexWhere((l) => l.type == 'meta' && l.text.contains('@@'));
      expect(hunk, greaterThanOrEqualTo(0));

      final adds = lines.where((l) => l.isAdd).toList();
      final removes = lines.where((l) => l.isRemove).toList();
      expect(adds, hasLength(2));
      expect(removes, hasLength(1));
      expect(removes.first.oldLine, 2);
      expect(adds.first.newLine, 2);
      expect(adds.last.newLine, 3);

      final contexts = lines.where((l) => l.type == 'context').toList();
      expect(contexts.first.text, 'package main');
    });

    test('handles multiple hunks', () {
      const patch = '''
@@ -1,2 +1,2 @@
-a
+b
@@ -10,2 +10,2 @@
-c
+d
''';
      final lines = parseUnifiedDiff(patch);
      final metas = lines.where((l) => l.isMeta).toList();
      expect(metas, hasLength(2));
      final removes = lines.where((l) => l.isRemove).toList();
      expect(removes.last.oldLine, 10);
      final adds = lines.where((l) => l.isAdd).toList();
      expect(adds.last.newLine, 10);
    });

    test('empty patch yields nothing renderable', () {
      expect(parseUnifiedDiff(''), isEmpty);
    });

    test('no-newline marker is meta', () {
      const patch = '''
@@ -1 +1 @@
-a
+b
\\ No newline at end of file
''';
      final lines = parseUnifiedDiff(patch);
      expect(lines.where((l) => l.isMeta && l.text.contains('No newline')),
          hasLength(1));
    });
  });
}

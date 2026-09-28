import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/shared/widgets/tool_view.dart';

void main() {
  group('toolSummary', () {
    test('picks the identifying argument per tool', () {
      expect(
        toolSummary('bash', {'command': 'go test ./...'}),
        'go test ./...',
      );
      expect(toolSummary('read', {'path': 'a/b.go', 'offset': 3}), 'a/b.go');
      expect(
        toolSummary('grep', {'pattern': 'Runner', 'path': 'internal'}),
        'Runner in internal',
      );
      expect(toolSummary('task', {'description': 'Explore'}), 'Explore');
    });

    test('multi-line commands read as their first line', () {
      expect(toolSummary('bash', {'command': 'cd x\nmake'}), 'cd x …');
    });

    test('unknown tools fall back to null', () {
      expect(toolSummary('mcp_thing', {'a': 1}), isNull);
      expect(toolSummary('bash', {}), isNull);
    });
  });

  test('lineDiff keeps shared lines as context around the change', () {
    final diff = lineDiff('a\nb\nc', 'a\nB\nc');
    expect(diff, [
      (DiffKind.context, 'a'),
      (DiffKind.remove, 'b'),
      (DiffKind.add, 'B'),
      (DiffKind.context, 'c'),
    ]);
  });

  test('classifyPatch reads apply_patch headers, adds and removes', () {
    final lines = classifyPatch(
      '*** Begin Patch\n*** Update File: x.go\n@@ func x\n ctx\n-old\n+new',
    );
    expect(lines.map((l) => l.$1), [
      DiffKind.header,
      DiffKind.header,
      DiffKind.header,
      DiffKind.context,
      DiffKind.remove,
      DiffKind.add,
    ]);
    expect(lines.last.$2, 'new');
  });

  test('languageForPath maps names and extensions', () {
    expect(languageForPath('internal/x.go'), 'go');
    expect(languageForPath('lib/main.dart'), 'dart');
    expect(languageForPath('Dockerfile'), 'dockerfile');
    expect(languageForPath('config.jsonc'), 'json');
    expect(languageForPath('README'), isNull);
  });

  testWidgets('read output renders as numbered, highlighted source', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.dark(),
        home: const Scaffold(
          body: ToolDetails(
            name: 'read',
            input: {'path': 'x.go', 'offset': 10, 'limit': 2},
            output: '10: package x\n11: func y() {}',
          ),
        ),
      ),
    );
    expect(find.text('10\n11'), findsOneWidget); // the gutter
    expect(
      find.textContaining('lines 10–11', findRichText: true),
      findsOneWidget,
    );
    // The "N: " prefixes are gone from the source itself.
    expect(
      find.textContaining('10: package', findRichText: true),
      findsNothing,
    );
  });

  testWidgets('unknown tools list their arguments, not JSON', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.dark(),
        home: const Scaffold(
          body: ToolDetails(
            name: 'mcp_lookup',
            input: {'city': 'Sofia', 'days': 3},
            output: '{"temp": 21}',
          ),
        ),
      ),
    );
    expect(find.text('city'), findsOneWidget);
    expect(find.text('Sofia'), findsOneWidget);
    expect(find.textContaining('"city"', findRichText: true), findsNothing);
  });
}

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/shared/widgets/message_parts.dart';

final _part = AssistantPart.fromJson({
  'type': 'tool',
  'id': 't1',
  'name': 'bash',
  'state': {
    'status': 'completed',
    'input': {'command': 'echo hi'},
    'output': 'hi there',
  },
});

Future<void> _pumpExpanded(WidgetTester tester) async {
  await tester.pumpWidget(
    MaterialApp(
      theme: AppTheme.dark(),
      home: Scaffold(body: ToolCallCard(part: _part)),
    ),
  );
  await tester.tap(find.textContaining('echo hi', findRichText: true).first);
  await tester.pumpAndSettle();
  expect(find.textContaining('hi there', findRichText: true), findsOneWidget);
}

Finder get _output => find.textContaining('hi there', findRichText: true);

void main() {
  testWidgets('a click on the expanded body collapses it', (tester) async {
    await _pumpExpanded(tester);
    await tester.tap(_output);
    await tester.pump(kDoubleTapTimeout + const Duration(milliseconds: 10));
    await tester.pumpAndSettle();
    expect(_output, findsNothing);
  });

  testWidgets('dragging (selecting text) keeps it open', (tester) async {
    await _pumpExpanded(tester);
    await tester.drag(_output, const Offset(60, 0));
    await tester.pump(kDoubleTapTimeout + const Duration(milliseconds: 10));
    await tester.pumpAndSettle();
    expect(_output, findsOneWidget);
  });

  testWidgets('a double-click keeps it open', (tester) async {
    await _pumpExpanded(tester);
    final at = tester.getCenter(_output);
    await tester.tapAt(at);
    await tester.pump(const Duration(milliseconds: 80));
    await tester.tapAt(at);
    await tester.pump(kDoubleTapTimeout + const Duration(milliseconds: 10));
    await tester.pumpAndSettle();
    expect(_output, findsOneWidget);
  });

  testWidgets('a triple-click keeps it open', (tester) async {
    await _pumpExpanded(tester);
    final at = tester.getCenter(_output);
    for (var i = 0; i < 3; i++) {
      await tester.tapAt(at);
      await tester.pump(const Duration(milliseconds: 80));
    }
    await tester.pump(kDoubleTapTimeout + const Duration(milliseconds: 10));
    await tester.pumpAndSettle();
    expect(_output, findsOneWidget);
  });
}

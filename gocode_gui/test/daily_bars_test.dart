import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/account.dart';
import 'package:gocode_gui/features/account/daily_bars.dart';

Future<void> _pump(WidgetTester tester, List<UsageDay> days, double width) {
  return tester.pumpWidget(
    MaterialApp(
      theme: AppTheme.dark(),
      home: Scaffold(
        body: Center(
          child: SizedBox(
            width: width,
            height: 190,
            child: DailyBars(days: days),
          ),
        ),
      ),
    ),
  );
}

List<UsageDay> _days(int count, int Function(int i) tokens) => [
  for (var i = 0; i < count; i++)
    UsageDay(
      date: DateTime(2026, 6, 1).add(Duration(days: i)),
      requests: tokens(i) ~/ 100,
      tokens: tokens(i),
    ),
];

void main() {
  testWidgets('90 days fit a narrow card, with a tooltip per day', (
    tester,
  ) async {
    await _pump(tester, _days(90, (i) => (i * 137) % 5000), 420);
    expect(tester.takeException(), isNull);

    // Clean axis: the ceiling above the 4,932 peak is 5k.
    expect(find.text('5k'), findsOneWidget);
    expect(find.text('0'), findsOneWidget);
    // First, middle and last dates.
    expect(find.text('Jun 1'), findsOneWidget);
    expect(find.text('Aug 29'), findsOneWidget);

    final tooltips = tester
        .widgetList<Tooltip>(find.byType(Tooltip))
        .map((t) => t.message)
        .toList();
    expect(tooltips, hasLength(90));
    expect(tooltips[1], 'Jun 2: 137 tokens · 1 requests');
  });

  testWidgets('an empty window says so instead of drawing', (tester) async {
    await _pump(tester, _days(30, (_) => 0), 600);
    expect(find.text('No usage in this period.'), findsOneWidget);
    expect(find.byType(Tooltip), findsNothing);
  });
}

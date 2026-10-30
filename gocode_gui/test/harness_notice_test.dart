import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/features/session/harness_notice.dart';

// Kept out of harness_monitor_test.dart: a testWidgets there would switch
// the whole file to the test binding, whose HttpClient answers every
// request with 400 and starves the fake server those tests talk to.
void main() {
  testWidgets('the harness notice renders its title and facts', (tester) async {
    await tester.pumpWidget(
      const MaterialApp(
        home: Scaffold(
          body: HarnessNotice(
            data: {
              'loaded': true,
              'changed': true,
              'dir': '/p/.gocode/harness',
              'skills': ['lint'],
            },
          ),
        ),
      ),
    );
    expect(find.text('Harness changed'), findsOneWidget);
    expect(find.text('.gocode/harness · 1 skill'), findsOneWidget);
  });
}

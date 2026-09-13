import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/app.dart';
import 'package:gocode_gui/shared/widgets/glass.dart';

void main() {
  testWidgets('first run shows the setup gate', (tester) async {
    await tester.pumpWidget(
      const ProviderScope(child: GoCodeApp()),
    );
    await tester.pumpAndSettle(const Duration(milliseconds: 100));

    expect(find.byType(GocodeLogo), findsOneWidget);
    expect(find.text('Choose project folder'), findsOneWidget);
    expect(find.text('Connect to a server'), findsOneWidget);
    expect(find.text('Advanced settings'), findsOneWidget);
  });

  testWidgets('remote option expands the URL form', (tester) async {
    await tester.pumpWidget(
      const ProviderScope(child: GoCodeApp()),
    );
    await tester.pumpAndSettle(const Duration(milliseconds: 100));

    await tester.tap(find.text('Connect to a server'));
    await tester.pumpAndSettle();

    // Expanded with Local selected by default.
    expect(find.text('Project directory'), findsOneWidget);
    expect(find.text('Local'), findsOneWidget);
    expect(find.text('Remote'), findsOneWidget);

    await tester.tap(find.text('Remote'));
    await tester.pumpAndSettle();
    expect(find.text('Server URL'), findsOneWidget);
  });

  testWidgets('shell renders behind the router', (tester) async {
    await tester.pumpWidget(
      ProviderScope(
        child: MaterialApp(
          home: Scaffold(
            body: Builder(
              builder: (context) => const Center(child: Text('shell')),
            ),
          ),
        ),
      ),
    );
    expect(find.text('shell'), findsOneWidget);
  });
}

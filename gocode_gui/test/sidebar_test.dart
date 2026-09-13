import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/account.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/features/account/providers.dart';
import 'package:gocode_gui/features/home/providers.dart';
import 'package:gocode_gui/features/sidebar/sidebar.dart';

Session _session(String id, String title, DateTime updated, {String? parent}) =>
    Session(
      id: id,
      title: title,
      directory: '/project',
      parentID: parent,
      timeCreated: updated.millisecondsSinceEpoch,
      timeUpdated: updated.millisecondsSinceEpoch,
    );

Future<void> _pump(WidgetTester tester, AccountInfo account) async {
  final now = DateTime.now();
  final sessions = [
    _session('s1', 'Fix the login bug', now),
    _session(
      's2',
      'Write release notes',
      now.subtract(const Duration(days: 1)),
    ),
    _session('sub', 'Subagent: explore', now, parent: 's1'),
  ];
  tester.view.physicalSize = const Size(1200, 900);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        sessionsProvider.overrideWith((ref) async => sessions),
        accountProvider.overrideWith((ref) async => account),
      ],
      child: MaterialApp.router(
        theme: AppTheme.dark(),
        routerConfig: GoRouter(
          initialLocation: '/session/s1',
          routes: [
            GoRoute(
              path: '/session/:id',
              // Like the app shell: no Scaffold or Material above the
              // sidebar, so it has to provide its own.
              builder: (context, state) => Row(
                children: [
                  SizedBox(
                    width: sidebarWidth,
                    child: Sidebar(location: state.uri.path),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('shows the history grouped, without subagent sessions', (
    tester,
  ) async {
    await _pump(
      tester,
      const AccountInfo(signedIn: false, site: 'https://gocoder.org'),
    );

    expect(find.text('TODAY'), findsOneWidget);
    expect(find.text('YESTERDAY'), findsOneWidget);
    expect(find.text('Fix the login bug'), findsOneWidget);
    expect(find.text('Write release notes'), findsOneWidget);
    expect(find.text('Subagent: explore'), findsNothing);
    expect(find.byKey(SidebarKeys.newSession), findsOneWidget);

    await tester.enterText(find.byType(TextField), 'release');
    await tester.pump();
    expect(find.text('Fix the login bug'), findsNothing);
    expect(find.text('Write release notes'), findsOneWidget);
  });

  testWidgets('account menu lists the account pages and sign-out', (
    tester,
  ) async {
    await _pump(
      tester,
      const AccountInfo(
        signedIn: true,
        site: 'https://gocoder.org',
        email: 'alice@example.com',
        displayName: 'Alice Liddell',
      ),
    );

    expect(find.text('Alice Liddell'), findsOneWidget);
    expect(find.text('AL'), findsOneWidget); // avatar initials

    await tester.tap(find.byKey(SidebarKeys.accountMenu));
    await tester.pumpAndSettle();
    for (final label in [
      'Profile',
      'User settings',
      'Settings',
      'Usage',
      'Invite a friend',
      'Sign out',
    ]) {
      expect(find.text(label), findsOneWidget, reason: label);
    }
    expect(find.text('Sign in'), findsNothing);
  });

  testWidgets('signed out, the menu offers sign-in instead', (tester) async {
    await _pump(
      tester,
      const AccountInfo(signedIn: false, site: 'https://gocoder.org'),
    );

    expect(find.text('Not signed in'), findsOneWidget);
    await tester.tap(find.byKey(SidebarKeys.accountMenu));
    await tester.pumpAndSettle();
    expect(find.text('Sign in'), findsOneWidget);
    expect(find.text('Sign out'), findsNothing);
  });
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/account.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/features/account/providers.dart';
import 'package:gocode_gui/features/home/providers.dart';
import 'package:gocode_gui/features/sidebar/projects.dart';
import 'package:gocode_gui/features/sidebar/sidebar.dart';
import 'package:shared_preferences/shared_preferences.dart';

Session _session(String id, String title, DateTime updated, {String? parent, String? directory}) =>
    Session(
      id: id,
      title: title,
      directory: directory ?? '/project',
      parentID: parent,
      timeCreated: updated.millisecondsSinceEpoch,
      timeUpdated: updated.millisecondsSinceEpoch,
    );

Future<void> _pump(
  WidgetTester tester,
  AccountInfo account, {
  List<Session>? sessions,
  // Element type left as `dynamic`: riverpod's `Override` is the field type
  // ProviderScope.overrides expects, but isn't part of its public exports,
  // so it can't be spelled here — dynamic elements still satisfy the spread
  // below via an implicit runtime cast.
  List<dynamic> extraOverrides = const [],
}) async {
  final now = DateTime.now();
  final seeded =
      sessions ??
      [
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
        sessionsProvider.overrideWith((ref) async => seeded),
        accountProvider.overrideWith((ref) async => account),
        ...extraOverrides,
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

/// Seeds [projectsProvider] without touching real persistence.
class _FixedProjects extends ProjectsNotifier {
  _FixedProjects(this._seed);

  final List<Project> _seed;

  @override
  List<Project> build() => _seed;
}

void main() {
  // Project persistence goes through real shared_preferences; without this
  // a plugin call with no test-mode handler just hangs forever.
  SharedPreferences.setMockInitialValues({});


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

  testWidgets('Projects tab groups a matching session under its project', (
    tester,
  ) async {
    final now = DateTime.now();
    final sessions = [
      _session('s1', 'Fix the login bug', now, directory: '/work/app'),
      _session(
        's2',
        'Unrelated chat',
        now.subtract(const Duration(hours: 1)),
        directory: '/somewhere/else',
      ),
    ];
    await _pump(
      tester,
      const AccountInfo(signedIn: false, site: 'https://gocoder.org'),
      sessions: sessions,
      extraOverrides: [
        projectsProvider.overrideWith(
          () => _FixedProjects([
            const Project(
              id: 'p1',
              name: 'My App',
              directory: '/work/app',
              timeCreated: 0,
            ),
          ]),
        ),
      ],
    );

    // Defaults to Chats: only the unmatched session shows there.
    expect(find.text('Unrelated chat'), findsOneWidget);
    expect(find.text('Fix the login bug'), findsNothing);

    await tester.tap(find.byKey(SidebarKeys.projectsTab));
    await tester.pumpAndSettle();
    expect(find.text('My App'), findsOneWidget);

    await tester.tap(find.text('My App'));
    await tester.pumpAndSettle();
    expect(find.text('Fix the login bug'), findsOneWidget);
    // Selecting the project surfaces the "new chats go here" chip.
    expect(find.textContaining('My App'), findsWidgets);
  });

  testWidgets('with no projects, the Projects tab explains itself', (
    tester,
  ) async {
    await _pump(
      tester,
      const AccountInfo(signedIn: false, site: 'https://gocoder.org'),
      extraOverrides: [projectsProvider.overrideWith(() => _FixedProjects([]))],
    );

    await tester.tap(find.byKey(SidebarKeys.projectsTab));
    await tester.pumpAndSettle();
    expect(
      find.textContaining('No projects yet'),
      findsOneWidget,
    );
    expect(find.byKey(SidebarKeys.newProject), findsOneWidget);
  });

  testWidgets('creating a project from the dialog selects it', (
    tester,
  ) async {
    await _pump(
      tester,
      const AccountInfo(signedIn: false, site: 'https://gocoder.org'),
      extraOverrides: [projectsProvider.overrideWith(() => _FixedProjects([]))],
    );

    await tester.tap(find.byKey(SidebarKeys.projectsTab));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(SidebarKeys.newProject));
    await tester.pumpAndSettle();

    final fields = find.descendant(
      of: find.byType(AlertDialog),
      matching: find.byType(TextField),
    );
    expect(fields, findsNWidgets(2));
    await tester.enterText(fields.at(1), '/work/newthing');
    await tester.pump();
    await tester.tap(find.widgetWithText(FilledButton, 'Create'));
    await tester.pumpAndSettle();

    // Dialog closed, project created and named from the folder, and
    // selected — so its "new chats go here" chip is visible.
    expect(find.byType(AlertDialog), findsNothing);
    expect(find.text('newthing'), findsWidgets);
  });
}

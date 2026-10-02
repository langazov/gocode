import 'package:flutter/material.dart' hide ConnectionState;
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:gocode_gui/app/app.dart';
import 'package:gocode_gui/app/sidebar_state.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/app/title_bar.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/sidebar/projects.dart';
import 'package:shared_preferences/shared_preferences.dart';

class _Connected extends AppConnection {
  @override
  ConnectionState build() =>
      const ConnectionState(phase: ConnectionPhase.connected);
}

class _Projects extends ProjectsNotifier {
  @override
  List<Project> build() => const [
    Project(id: 'p', name: 'gocode', directory: '/src/gocode', timeCreated: 0),
  ];
}

class _Selected extends SelectedProjectNotifier {
  @override
  String? build() => 'p';
}

Future<(GoRouter, ProviderContainer)> _pump(WidgetTester tester) async {
  tester.view.physicalSize = const Size(1200, 700);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  Widget page(String name) => Text('page $name');
  final router = GoRouter(
    initialLocation: '/',
    routes: [
      GoRoute(path: '/', builder: (_, _) => page('home')),
      GoRoute(path: '/new', builder: (_, _) => page('new')),
      GoRoute(path: '/git', builder: (_, _) => page('git')),
      GoRoute(path: '/settings', builder: (_, _) => page('settings')),
    ],
  );
  addTearDown(router.dispose);
  final container = ProviderContainer(
    overrides: [
      connectionProvider.overrideWith(_Connected.new),
      routerProvider.overrideWithValue(router),
      projectsProvider.overrideWith(_Projects.new),
      selectedProjectProvider.overrideWith(_Selected.new),
      titleBranchProvider.overrideWith((ref, dir) => Stream.value('main')),
    ],
  );
  addTearDown(container.dispose);
  await tester.pumpWidget(
    UncontrolledProviderScope(
      container: container,
      child: MaterialApp.router(
        theme: AppTheme.dark(),
        routerConfig: router,
        builder: (context, child) => TitleBarFrame(child: child!),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return (router, container);
}

void main() {
  SharedPreferences.setMockInitialValues({});

  testWidgets('shows the project and branch above the page', (tester) async {
    await _pump(tester);

    expect(find.text('gocode'), findsOneWidget);
    expect(find.text('main'), findsOneWidget);
    expect(find.text('Server'), findsOneWidget); // connection mode
    expect(find.text('page home'), findsOneWidget);
    // The page sits below the bar, not under it.
    expect(
      tester.getTopLeft(find.text('page home')).dy,
      greaterThanOrEqualTo(titleBarHeight),
    );
  });

  testWidgets('destinations navigate', (tester) async {
    final (router, _) = await _pump(tester);

    await tester.tap(find.text('New'));
    await tester.pumpAndSettle();
    expect(find.text('page new'), findsOneWidget);

    await tester.tap(find.byTooltip('Source control').first);
    await tester.pumpAndSettle();
    expect(find.text('page git'), findsOneWidget);
    expect(
      router.routerDelegate.currentConfiguration.uri.queryParameters['dir'],
      '/src/gocode',
    );

    await tester.tap(find.byTooltip('Settings'));
    await tester.pumpAndSettle();
    expect(find.text('page settings'), findsOneWidget);
  });

  testWidgets('sidebar button hides and shows the docked sidebar', (
    tester,
  ) async {
    final (_, container) = await _pump(tester);

    await tester.tap(find.byTooltip('Hide sidebar'));
    await tester.pump();
    expect(container.read(sidebarProvider).hidden, isTrue);

    await tester.tap(find.byTooltip('Show sidebar'));
    await tester.pump();
    expect(container.read(sidebarProvider).hidden, isFalse);
  });
}

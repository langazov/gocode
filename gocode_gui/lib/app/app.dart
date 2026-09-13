import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../core/connection/controller.dart';
import '../features/asks/asks.dart';
import '../features/home/home_screen.dart';
import '../features/home/new_session_screen.dart';
import '../features/session/session_screen.dart';
import '../features/settings/settings_screen.dart';
import '../shared/widgets/glass.dart';
import 'connect_screen.dart';
import 'theme.dart';

/// The app router. Its redirect re-runs whenever the connection phase
/// changes, so the connect gate opens and closes in place — re-keying the
/// whole MaterialApp instead would drop every route's state.
final routerProvider = Provider<GoRouter>((ref) {
  final phase = ValueNotifier(ref.read(connectionProvider).phase);
  ref.listen(
    connectionProvider.select((s) => s.phase),
    (_, next) => phase.value = next,
  );

  // Each page paints its own ground, so route transitions stay opaque.
  Page<void> page(GoRouterState state, Widget child) => MaterialPage<void>(
        key: state.pageKey,
        child: AmbientBackground(child: child),
      );

  final router = GoRouter(
    refreshListenable: phase,
    redirect: (context, state) {
      final connected = ref.read(connectionProvider).isConnected;
      final location = state.matchedLocation;
      if (!connected && location != '/connect' && location != '/settings') {
        return '/connect';
      }
      if (connected && location == '/connect') return '/';
      return null;
    },
    routes: [
      GoRoute(
        path: '/connect',
        pageBuilder: (context, state) => page(state, const ConnectScreen()),
      ),
      GoRoute(
        path: '/',
        pageBuilder: (context, state) => page(state, const HomeScreen()),
      ),
      GoRoute(
        path: '/new',
        pageBuilder: (context, state) => page(state, const NewSessionScreen()),
      ),
      GoRoute(
        path: '/settings',
        pageBuilder: (context, state) => page(state, const SettingsScreen()),
      ),
      GoRoute(
        path: '/session/:id',
        pageBuilder: (context, state) => page(
          state,
          SessionScreen(sessionID: state.pathParameters['id']!),
        ),
      ),
    ],
  );
  ref.onDispose(() {
    router.dispose();
    phase.dispose();
  });
  return router;
});

class GoCodeApp extends ConsumerWidget {
  const GoCodeApp({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final router = ref.watch(routerProvider);
    return MaterialApp.router(
      title: 'gocode',
      debugShowCheckedModeBanner: false,
      theme: AppTheme.dark(),
      themeMode: ThemeMode.dark,
      routerConfig: router,
      // Asks open sheets on the router's navigator; this builder sits above
      // it, so it gets the navigator by key rather than by context.
      builder: (context, child) => AsksOverlay(
        navigatorKey: router.routerDelegate.navigatorKey,
        child: child ?? const SizedBox.shrink(),
      ),
    );
  }
}

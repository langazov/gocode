import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../core/connection/controller.dart';
import '../features/account/invite_screen.dart';
import '../features/account/profile_screen.dart';
import '../features/account/usage_screen.dart';
import '../features/account/user_settings_screen.dart';
import '../features/asks/asks.dart';
import '../features/home/home_screen.dart';
import '../features/home/new_session_screen.dart';
import '../features/session/session_screen.dart';
import '../features/settings/settings_screen.dart';
import '../shared/widgets/glass.dart';
import 'connect_screen.dart';
import 'shell.dart';
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

  GoRoute route(String path, Widget Function(GoRouterState state) build) =>
      GoRoute(
        path: path,
        pageBuilder: (context, state) => page(state, build(state)),
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
      route('/connect', (_) => const ConnectScreen()),
      // Everything past the gate shares the shell: sidebar beside the page.
      ShellRoute(
        builder: (context, state, child) =>
            AppShell(location: state.uri.path, child: child),
        routes: [
          route('/', (_) => const HomeScreen()),
          route('/new', (_) => const NewSessionScreen()),
          route('/settings', (_) => const SettingsScreen()),
          route(
            '/session/:id',
            (state) => SessionScreen(sessionID: state.pathParameters['id']!),
          ),
          route('/account/profile', (_) => const ProfileScreen()),
          route('/account/settings', (_) => const UserSettingsScreen()),
          route('/account/usage', (_) => const UsageScreen()),
          route('/account/invite', (_) => const InviteScreen()),
        ],
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
      title: 'Gocode Desktop',
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

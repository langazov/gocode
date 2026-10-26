import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../core/api/harness_models.dart' show harnessMoney;
import '../core/connection/controller.dart';
import '../features/harness/harness_state.dart';
import '../features/sidebar/sidebar.dart';
import 'sidebar_scope.dart';
import 'sidebar_state.dart';
import 'theme.dart';
import 'window_chrome.dart' show showsAppTitleBar;

/// The window layout while connected: the session-history sidebar docked on
/// the left, the routed page beside it. Narrower than [_wideBreakpoint], the
/// sidebar becomes a drawer; on wide windows it can be hidden, and the
/// header's menu button brings it back either way.
class AppShell extends ConsumerStatefulWidget {
  const AppShell({super.key, required this.location, required this.child});

  /// The current route path, for the sidebar's highlight.
  final String location;
  final Widget child;

  @override
  ConsumerState<AppShell> createState() => _AppShellState();
}

class _AppShellState extends ConsumerState<AppShell> {
  final _scaffold = GlobalKey<ScaffoldState>();

  @override
  Widget build(BuildContext context) {
    final connected = ref.watch(
      connectionProvider.select((s) => s.isConnected),
    );
    // Settings is reachable before connecting; there's no history to show.
    if (!connected) return widget.child;
    final hidden = ref.watch(sidebarProvider.select((s) => s.hidden));
    // The title bar's sidebar button asks for the drawer on narrow windows.
    ref.listen(
      sidebarProvider.select((s) => s.drawerRequests),
      (_, _) => _scaffold.currentState?.openDrawer(),
    );
    _listenHarnessEnd();

    return LayoutBuilder(
      builder: (context, constraints) {
        final wide = constraints.maxWidth >= sidebarWideBreakpoint;
        final docked = wide && !hidden;
        final page = SidebarScope(
          // With the app's title bar, its sidebar button is the way back;
          // pages don't add their own.
          visible: docked || showsAppTitleBar,
          onOpen: () => ref.read(sidebarProvider.notifier).toggle(wide: wide),
          child: widget.child,
        );

        if (wide) {
          return ColoredBox(
            color: GC.bgPage,
            child: Row(
              children: [
                AnimatedContainer(
                  duration: GC.dur,
                  curve: GC.ease,
                  width: docked ? sidebarWidth : 0,
                  child: ClipRect(
                    child: OverflowBox(
                      alignment: Alignment.centerRight,
                      minWidth: sidebarWidth,
                      maxWidth: sidebarWidth,
                      child: Sidebar(
                        location: widget.location,
                        onHide: showsAppTitleBar
                            ? null
                            : ref.read(sidebarProvider.notifier).hide,
                      ),
                    ),
                  ),
                ),
                Expanded(child: page),
              ],
            ),
          );
        }
        return Scaffold(
          key: _scaffold,
          backgroundColor: GC.bgPage,
          drawer: Drawer(
            width: sidebarWidth,
            backgroundColor: GC.surface1,
            shape: const RoundedRectangleBorder(),
            child: Sidebar(
              location: widget.location,
              onNavigate: () => _scaffold.currentState?.closeDrawer(),
            ),
          ),
          body: page,
        );
      },
    );
  }

  /// A run's end is announced once, never repeated on rebuild — the TUI's
  /// toast (documentation/11-desktop.md, Harness²).
  void _listenHarnessEnd() {
    ref.listen(harnessRunsProvider.select((s) => s.lastEnded), (prev, next) {
      if (next == null || next.noticeID.isEmpty) return;
      if (prev?.noticeID == next.noticeID) return;
      final context = this.context;
      final run = next.run;
      final ok = run.status == 'ok';
      final message = switch (run.status) {
        'ok' => 'adopted ${run.adopted} · ${harnessMoney(run.costUSD)}',
        'cancelled' => 'cancelled · finished work is kept for a resume',
        _ => run.error.isEmpty ? run.status : '${run.status}: ${run.error}',
      };
      final messenger = ScaffoldMessenger.maybeOf(context);
      messenger?.showSnackBar(
        SnackBar(
          backgroundColor: ok ? GC.surface3 : GC.down.withValues(alpha: .2),
          content: Text(
            'Harness² · ${run.task} · $message',
            style: TextStyle(color: ok ? GC.textHi : GC.downText),
          ),
          action: SnackBarAction(
            label: 'Open',
            onPressed: () => GoRouter.of(context).go('/harness/run/${run.id}'),
          ),
        ),
      );
    });
  }
}

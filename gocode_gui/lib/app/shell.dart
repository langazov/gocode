import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/connection/controller.dart';
import '../features/sidebar/sidebar.dart';
import 'sidebar_scope.dart';
import 'theme.dart';

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
  static const _wideBreakpoint = 900.0;
  final _scaffold = GlobalKey<ScaffoldState>();
  bool _hidden = false;

  @override
  Widget build(BuildContext context) {
    final connected = ref.watch(
      connectionProvider.select((s) => s.isConnected),
    );
    // Settings is reachable before connecting; there's no history to show.
    if (!connected) return widget.child;

    return LayoutBuilder(
      builder: (context, constraints) {
        final wide = constraints.maxWidth >= _wideBreakpoint;
        final docked = wide && !_hidden;
        final page = SidebarScope(
          visible: docked,
          onOpen: () {
            if (wide) {
              setState(() => _hidden = false);
            } else {
              _scaffold.currentState?.openDrawer();
            }
          },
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
                        onHide: () => setState(() => _hidden = true),
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
}

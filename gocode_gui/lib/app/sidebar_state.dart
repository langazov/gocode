import 'package:flutter_riverpod/flutter_riverpod.dart';

/// Window width at which the sidebar docks beside the page; narrower, it is
/// a drawer.
const sidebarWideBreakpoint = 900.0;

/// Whether the docked sidebar is hidden, plus a counter the shell watches
/// to open the drawer on narrow windows. Shared so the title bar can drive
/// the sidebar the shell lays out.
class SidebarState {
  const SidebarState({this.hidden = false, this.drawerRequests = 0});

  final bool hidden;
  final int drawerRequests;
}

class SidebarNotifier extends Notifier<SidebarState> {
  @override
  SidebarState build() => const SidebarState();

  void hide() =>
      state = SidebarState(hidden: true, drawerRequests: state.drawerRequests);

  void show() => state = SidebarState(drawerRequests: state.drawerRequests);

  /// Docked ([wide]): flip hidden. Narrow: ask the shell for the drawer.
  void toggle({required bool wide}) {
    if (!wide) {
      state = SidebarState(
        hidden: state.hidden,
        drawerRequests: state.drawerRequests + 1,
      );
    } else if (state.hidden) {
      show();
    } else {
      hide();
    }
  }
}

final sidebarProvider = NotifierProvider<SidebarNotifier, SidebarState>(
  SidebarNotifier.new,
);

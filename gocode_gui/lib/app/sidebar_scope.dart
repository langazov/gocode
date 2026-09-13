import 'package:flutter/widgets.dart';

/// Tells pages under the app shell whether the sidebar is on screen, and how
/// to bring it back — the floating header shows a menu button when it isn't.
class SidebarScope extends InheritedWidget {
  const SidebarScope({
    super.key,
    required this.visible,
    required this.onOpen,
    required super.child,
  });

  final bool visible;
  final VoidCallback onOpen;

  static SidebarScope? maybeOf(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<SidebarScope>();

  @override
  bool updateShouldNotify(SidebarScope oldWidget) =>
      oldWidget.visible != visible;
}

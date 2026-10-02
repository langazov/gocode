/// The window's title bar (on macOS it shares the row with the traffic
/// lights): sidebar toggle and folder / branch breadcrumb on the left, the
/// app's main destinations and the connection on the right. Empty space
/// drags the window. After goide (app/lib/ui/title_bar.dart).
library;

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../core/connection/controller.dart';
import '../features/git/git_screen.dart' show sourceControlLocation;
import '../features/sidebar/projects.dart';
import 'app.dart' show routerProvider;
import 'sidebar_state.dart';
import 'theme.dart';
import 'window_chrome.dart';

/// Height of the bar; macos/Runner/MainFlutterWindow.swift centers the
/// traffic lights in the same height.
const double titleBarHeight = 40;
const double _controlHeight = 28;

/// Puts the title bar above [child] (the router's navigator) in an overlay
/// of its own, so the bar's menus and tooltips have somewhere to open.
class TitleBarFrame extends StatefulWidget {
  const TitleBarFrame({super.key, required this.child});

  final Widget child;

  @override
  State<TitleBarFrame> createState() => _TitleBarFrameState();
}

class _TitleBarFrameState extends State<TitleBarFrame> {
  late final OverlayEntry _entry = OverlayEntry(
    builder: (context) => Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const TitleBar(),
        Expanded(
          // The bar already clears the window's top edge.
          child: MediaQuery.removePadding(
            context: context,
            removeTop: true,
            child: widget.child,
          ),
        ),
      ],
    ),
  );

  @override
  void didUpdateWidget(TitleBarFrame oldWidget) {
    super.didUpdateWidget(oldWidget);
    _entry.markNeedsBuild();
  }

  @override
  void dispose() {
    _entry
      ..remove()
      ..dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => Overlay(initialEntries: [_entry]);
}

class TitleBar extends ConsumerStatefulWidget {
  const TitleBar({super.key});

  @override
  ConsumerState<TitleBar> createState() => _TitleBarState();
}

class _TitleBarState extends ConsumerState<TitleBar> {
  // Like a native title bar, the bar recedes while the window is in the
  // background.
  late final AppLifecycleListener _lifecycle;
  bool _active = true;

  /// The router's current path, for the selected destination. go_router
  /// notifies mid-build (redirects), so updates land after the frame.
  GoRouter? _router;
  String _path = '';

  @override
  void initState() {
    super.initState();
    _lifecycle = AppLifecycleListener(
      onStateChange: (s) {
        final active = s == AppLifecycleState.resumed;
        if (active != _active) setState(() => _active = active);
      },
    );
  }

  void _watchRouter(GoRouter router) {
    if (identical(router, _router)) return;
    _router?.routerDelegate.removeListener(_onRoute);
    _router = router..routerDelegate.addListener(_onRoute);
    _path = _currentPath(router);
    // The first route may resolve before this listener was attached.
    _onRoute();
  }

  static String _currentPath(GoRouter router) {
    try {
      return router.routerDelegate.currentConfiguration.uri.path;
    } catch (_) {
      return ''; // before the first route resolves
    }
  }

  void _onRoute() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final router = _router;
      if (!mounted || router == null) return;
      final path = _currentPath(router);
      if (path != _path) setState(() => _path = path);
    });
  }

  @override
  void dispose() {
    _router?.routerDelegate.removeListener(_onRoute);
    _lifecycle.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final connected = ref.watch(
      connectionProvider.select((s) => s.isConnected),
    );
    // On macOS the traffic lights sit at the bar's left (hidden in full
    // screen).
    final lights =
        hasNativeTrafficLights && !ref.watch(windowFullScreenProvider);
    final left = lights ? ref.watch(trafficLightsInsetProvider) : 10.0;
    final router = ref.watch(routerProvider);
    _watchRouter(router);
    final path = _path;

    return Material(
      color: GC.surface1,
      child: Container(
        height: titleBarHeight,
        decoration: const BoxDecoration(
          border: Border(bottom: BorderSide(color: GC.border)),
        ),
        child: Stack(
          fit: StackFit.expand,
          children: [
            // Behind the controls: empty space drags the window.
            const Positioned.fill(child: WindowDragArea()),
            AnimatedOpacity(
              opacity: _active ? 1 : 0.55,
              duration: const Duration(milliseconds: 150),
              child: Padding(
                padding: EdgeInsets.only(left: left, right: 8),
                child: Builder(
                  builder: (context) {
                    return Row(
                      children: [
                        if (connected) ...[
                          const _SidebarToggle(),
                          const SizedBox(width: 4),
                          const Flexible(child: _Breadcrumb()),
                        ] else
                          const _AppName(),
                        const Spacer(),
                        if (connected) ...[
                          _BarButton(
                            tooltip: 'New session',
                            icon: Icons.add_rounded,
                            label: 'New',
                            selected: path == '/new',
                            onTap: () => router.go('/new'),
                          ),
                          const SizedBox(width: 2),
                          _BarButton(
                            tooltip: 'Source control',
                            icon: Icons.account_tree_outlined,
                            selected: path == '/git',
                            onTap: () => router.go(
                              sourceControlLocation(
                                directory: _selectedDirectory(ref),
                              ),
                            ),
                          ),
                          const SizedBox(width: 6),
                          // Not connected, the connect screen says so.
                          const _ConnectionChip(),
                          const SizedBox(width: 2),
                        ],
                        _BarButton(
                          tooltip: 'Settings',
                          icon: Icons.tune_rounded,
                          selected: path == '/settings',
                          onTap: () => router.go('/settings'),
                        ),
                      ],
                    );
                  },
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// The folder the breadcrumb names: the selected project's, else the
/// connection's working directory (null: the server decides).
String? _selectedDirectory(WidgetRef ref) {
  final project = findProject(
    ref.watch(projectsProvider),
    ref.watch(selectedProjectProvider),
  );
  return project?.directory;
}

/// A borderless title-bar control: no ink, a quiet hover fill.
class _BarButton extends StatefulWidget {
  const _BarButton({
    required this.icon,
    required this.onTap,
    this.label,
    this.tooltip,
    this.selected = false,
  });

  final IconData icon;
  final String? label;
  final VoidCallback? onTap;
  final String? tooltip;
  final bool selected;

  @override
  State<_BarButton> createState() => _BarButtonState();
}

class _BarButtonState extends State<_BarButton> {
  bool _hover = false;

  @override
  Widget build(BuildContext context) => _BarChip(
    tooltip: widget.tooltip,
    selected: widget.selected,
    onTap: widget.onTap,
    onHover: (h) => setState(() => _hover = h),
    padding: EdgeInsets.symmetric(horizontal: widget.label == null ? 6 : 9),
    child: Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(
          widget.icon,
          size: 17,
          color: widget.selected
              ? GC.accentText
              : (_hover ? GC.textHi : GC.textBody),
        ),
        if (widget.label != null) ...[
          const SizedBox(width: 5),
          Text(
            widget.label!,
            style: TextStyle(
              fontFamily: GC.sans,
              fontSize: 13,
              fontWeight: FontWeight.w600,
              color: widget.selected ? GC.accentText : GC.textHi,
            ),
          ),
        ],
      ],
    ),
  );
}

/// The hover/press plate every bar control sits on.
class _BarChip extends StatefulWidget {
  const _BarChip({
    required this.child,
    required this.onTap,
    this.tooltip,
    this.selected = false,
    this.onHover,
    this.padding = const EdgeInsets.symmetric(horizontal: 8),
  });

  final Widget child;
  final VoidCallback? onTap;
  final String? tooltip;
  final bool selected;
  final ValueChanged<bool>? onHover;
  final EdgeInsets padding;

  @override
  State<_BarChip> createState() => _BarChipState();
}

class _BarChipState extends State<_BarChip> {
  bool _hover = false;
  bool _down = false;

  void _setHover(bool h) {
    setState(() {
      _hover = h;
      if (!h) _down = false;
    });
    widget.onHover?.call(h);
  }

  @override
  Widget build(BuildContext context) {
    final bg = _down
        ? GC.surface3
        : _hover
        ? GC.surface2
        : widget.selected
        ? GC.accent.withValues(alpha: 0.12)
        : Colors.transparent;
    Widget chip = MouseRegion(
      onEnter: (_) => _setHover(true),
      onExit: (_) => _setHover(false),
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTapDown: (_) => setState(() => _down = true),
        onTapCancel: () => setState(() => _down = false),
        onTapUp: (_) => setState(() => _down = false),
        onTap: widget.onTap,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 90),
          height: _controlHeight,
          padding: widget.padding,
          decoration: BoxDecoration(
            color: bg,
            borderRadius: BorderRadius.circular(7),
          ),
          child: Center(widthFactor: 1, child: widget.child),
        ),
      ),
    );
    if (widget.tooltip != null) {
      chip = Tooltip(message: widget.tooltip!, child: chip);
    }
    return Semantics(button: true, child: chip);
  }
}

class _AppName extends StatelessWidget {
  const _AppName();

  @override
  Widget build(BuildContext context) => const Text(
    'Gocode Desktop',
    style: TextStyle(
      fontFamily: GC.sans,
      fontSize: 13,
      fontWeight: FontWeight.w600,
      color: GC.textBody,
    ),
  );
}

class _SidebarToggle extends ConsumerWidget {
  const _SidebarToggle();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final wide = MediaQuery.sizeOf(context).width >= sidebarWideBreakpoint;
    final hidden = ref.watch(sidebarProvider.select((s) => s.hidden));
    return _BarButton(
      tooltip: wide && !hidden ? 'Hide sidebar' : 'Show sidebar',
      icon: Icons.view_sidebar_outlined,
      onTap: () => ref.read(sidebarProvider.notifier).toggle(wide: wide),
    );
  }
}

/// `folder ▾ / ⎇ branch`: the project menu and Source Control.
class _Breadcrumb extends ConsumerWidget {
  const _Breadcrumb();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final directory = _selectedDirectory(ref);
    final branch = ref.watch(titleBranchProvider(directory)).value;
    return LayoutBuilder(
      builder: (context, c) => Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Flexible(child: _FolderMenu()),
          // The branch goes first when space runs out.
          if (branch != null && branch.isNotEmpty && c.maxWidth >= 200) ...[
            const Padding(
              padding: EdgeInsets.symmetric(horizontal: 2),
              child: Text(
                '/',
                style: TextStyle(fontSize: 13, color: GC.textFaint),
              ),
            ),
            Flexible(
              child: _BarChip(
                tooltip: 'Source control',
                onTap: () => ref
                    .read(routerProvider)
                    .go(sourceControlLocation(directory: directory)),
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    const Icon(
                      Icons.call_split_rounded,
                      size: 15,
                      color: GC.textDim,
                    ),
                    const SizedBox(width: 5),
                    Flexible(
                      child: Text(
                        branch,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                          fontFamily: GC.sans,
                          fontSize: 13,
                          color: GC.textBody,
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ],
        ],
      ),
    );
  }
}

/// The folder name with a menu of the user's projects.
class _FolderMenu extends ConsumerWidget {
  const _FolderMenu();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final projects = ref.watch(projectsProvider);
    final selectedID = ref.watch(selectedProjectProvider);
    final selected = findProject(projects, selectedID);
    final settings = ref.watch(settingsProvider);
    final fallback = settings.mode == ConnectionMode.remote
        ? (Uri.tryParse(settings.remoteUrl)?.host ?? 'Remote')
        : settings.workingDirectory.isEmpty
        ? 'gocode'
        : nameFromDirectory(settings.workingDirectory);
    final label = selected == null
        ? fallback
        : (selected.name.isEmpty
              ? nameFromDirectory(selected.directory)
              : selected.name);

    MenuItemButton item(String text, bool checked, VoidCallback onPressed) =>
        MenuItemButton(
          leadingIcon: Icon(
            checked ? Icons.check_rounded : Icons.folder_outlined,
            size: 16,
            color: checked ? GC.accentText : GC.textDim,
          ),
          onPressed: onPressed,
          child: Text(text),
        );

    return MenuAnchor(
      alignmentOffset: const Offset(0, 6),
      menuChildren: [
        item(
          'All projects',
          selected == null,
          ref.read(selectedProjectProvider.notifier).clear,
        ),
        if (projects.isNotEmpty) const Divider(height: 8),
        for (final p in projects)
          item(
            p.name.isEmpty ? nameFromDirectory(p.directory) : p.name,
            p.id == selectedID,
            () => ref.read(selectedProjectProvider.notifier).select(p.id),
          ),
      ],
      builder: (context, controller, _) => _BarChip(
        tooltip: selected?.directory ?? 'Switch project',
        onTap: () => controller.isOpen ? controller.close() : controller.open(),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.folder_rounded, size: 15, color: GC.accentText),
            const SizedBox(width: 6),
            Flexible(
              child: Text(
                label,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(
                  fontFamily: GC.sans,
                  fontSize: 13,
                  fontWeight: FontWeight.w600,
                  color: GC.textHi,
                ),
              ),
            ),
            const SizedBox(width: 2),
            const Icon(Icons.expand_more_rounded, size: 16, color: GC.textDim),
          ],
        ),
      ),
    );
  }
}

/// Mode and health of the connection; opens Settings.
class _ConnectionChip extends ConsumerWidget {
  const _ConnectionChip();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final conn = ref.watch(connectionProvider);
    final mode = ref.watch(settingsProvider.select((s) => s.mode));
    final (color, state) = switch (conn.phase) {
      ConnectionPhase.connected => (GC.ok, 'Connected'),
      ConnectionPhase.connecting => (GC.warn, 'Connecting…'),
      ConnectionPhase.error => (GC.down, 'Connection failed'),
      ConnectionPhase.disconnected => (GC.textFaint, 'Not connected'),
    };
    final modeName = switch (mode) {
      ConnectionMode.local => 'Server',
      ConnectionMode.acp => 'ACP',
      ConnectionMode.remote => 'Remote',
    };
    return _BarChip(
      tooltip: [
        state,
        if (conn.baseUrl != null) conn.baseUrl!,
        if (conn.error != null) conn.error!,
      ].join('\n'),
      onTap: () => ref.read(routerProvider).go('/settings'),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Container(
            width: 7,
            height: 7,
            decoration: BoxDecoration(color: color, shape: BoxShape.circle),
          ),
          const SizedBox(width: 6),
          Text(
            modeName,
            style: const TextStyle(
              fontFamily: GC.sans,
              fontSize: 12.5,
              fontWeight: FontWeight.w500,
              color: GC.textBody,
            ),
          ),
        ],
      ),
    );
  }
}

/// The breadcrumb's branch for [directory] (null: the server's folder):
/// polled while the bar shows it, null outside a repository or without an
/// HTTP API (ACP mode).
final titleBranchProvider = StreamProvider.autoDispose.family<String?, String?>(
  (ref, directory) {
    final client = ref.watch(apiClientProvider);
    if (client == null) return Stream.value(null);
    final controller = StreamController<String?>();
    Future<void> poll() async {
      try {
        final st = await client.gitStatus(directory: directory);
        if (!controller.isClosed) {
          controller.add(
            !st.isRepo
                ? null
                : st.detached
                ? st.head
                : st.branch,
          );
        }
      } catch (_) {
        if (!controller.isClosed) controller.add(null);
      }
    }

    unawaited(poll());
    final timer = Timer.periodic(const Duration(seconds: 10), (_) => poll());
    ref.onDispose(() {
      timer.cancel();
      controller.close();
    });
    return controller.stream;
  },
);

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import '../../core/connection/controller.dart';
import '../../core/connection/session_activity.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/session_status.dart';
import '../account/avatar.dart';
import '../account/providers.dart';
import '../git/git_screen.dart' show sourceControlLocation;
import '../home/providers.dart';
import '../session/timeline.dart' show acpConnectionProvider;
import 'history.dart';
import 'project_grouping.dart';
import 'projects.dart';

/// Width of the docked sidebar (and the drawer on narrow windows).
const sidebarWidth = 256.0;

/// Keys tests reach sidebar controls by.
abstract final class SidebarKeys {
  static const newSession = ValueKey('sidebar-new-session');
  static const accountMenu = ValueKey('sidebar-account-menu');
  static const projectsTab = ValueKey('sidebar-projects-tab');
  static const chatsTab = ValueKey('sidebar-chats-tab');
  static const newProject = ValueKey('sidebar-new-project');
  static const sourceControl = ValueKey('sidebar-source-control');
}

/// The left navigation: new session, a Projects/Chats tab pair, and the
/// account menu at the bottom.
///
/// Projects group chats by work folder (see [groupByProject]); a chat with
/// no matching project lives in Chats, grouped by recency like before.
class Sidebar extends ConsumerStatefulWidget {
  const Sidebar({
    super.key,
    required this.location,
    this.onHide,
    this.onNavigate,
  });

  /// The current route path, for highlighting the open session.
  final String location;

  /// Hides the docked sidebar (wide windows only).
  final VoidCallback? onHide;

  /// Called before navigating, e.g. to close the drawer.
  final VoidCallback? onNavigate;

  @override
  ConsumerState<Sidebar> createState() => _SidebarState();
}

class _SidebarState extends ConsumerState<Sidebar> {
  final _search = TextEditingController();
  String _query = '';

  // 0 = Projects, 1 = Chats. Most installs start with no projects yet;
  // defaulting to Chats means a returning user's existing chats are never
  // hidden behind an empty tab.
  int _tab = 1;

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  void didUpdateWidget(Sidebar oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.location != widget.location) {
      // Moving between pages is when titles change and sessions appear, so
      // refetch then rather than poll. After the frame: a provider can't be
      // invalidated while the tree is building.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) ref.invalidate(sessionsProvider);
      });
    }
  }

  String? get _activeSessionID {
    const prefix = '/session/';
    final location = widget.location;
    return location.startsWith(prefix)
        ? location.substring(prefix.length)
        : null;
  }

  void _go(String path) {
    widget.onNavigate?.call();
    context.go(path);
  }

  Future<void> _act(String action, Session session) async {
    try {
      final acp = ref.read(acpConnectionProvider);
      switch (action) {
        case 'rename':
          final title = await _promptTitle(session.title);
          if (title == null || title.trim().isEmpty) return;
          if (acp != null) {
            await _renameAcp(acp, session.id, title.trim());
            break;
          }
          final client = ref.read(apiClientProvider);
          if (client == null) return;
          await client.renameSession(session.id, title.trim());
        case 'fork':
          if (acp != null) {
            // Forking over ACP is a gocode extension the client does not
            // exercise yet; sessions can still be continued by resuming.
            throw UnsupportedError('fork is not available in ACP mode');
          }
          final client = ref.read(apiClientProvider);
          if (client == null) return;
          await client.forkSession(session.id);
        case 'delete':
          if (await _confirmDelete(session.title) != true) return;
          if (acp != null) {
            await acp.deleteSession(session.id);
            if (mounted && _activeSessionID == session.id) context.go('/');
            break;
          }
          final client = ref.read(apiClientProvider);
          if (client == null) return;
          await client.deleteSession(session.id);
          if (mounted && _activeSessionID == session.id) context.go('/');
      }
      ref.invalidate(sessionsProvider);
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }

  /// ACP has no rename; the title the agent generated stands. Kept honest
  /// rather than pretending: shows the limitation once.
  Future<void> _renameAcp(dynamic acp, String id, String title) async {
    throw UnsupportedError('renaming is not available in ACP mode');
  }

  Future<String?> _promptTitle(String current) {
    final controller = TextEditingController(text: current);
    return showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Rename session'),
        content: SizedBox(
          width: 420,
          child: TextField(
            controller: controller,
            autofocus: true,
            onSubmitted: (value) => Navigator.pop(context, value),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, controller.text),
            child: const Text('Save'),
          ),
        ],
      ),
    );
  }

  Future<bool?> _confirmDelete(String title) {
    return showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Delete session?'),
        content: Text(
          '"${title.isEmpty ? 'Untitled' : title}" will be removed permanently.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            style: FilledButton.styleFrom(
              backgroundColor: GC.down,
              foregroundColor: GC.textHi,
            ),
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Delete'),
          ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final selectedProject = findProject(
      ref.watch(projectsProvider),
      ref.watch(selectedProjectProvider),
    );

    // Its own Material: docked, the sidebar sits in a plain Row with no
    // Scaffold above it, and its text field, rows and menus all need one.
    return Material(
      color: GC.surface1,
      shape: const Border(right: BorderSide(color: GC.border)),
      child: SafeArea(
        right: false,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 8, 10),
              child: Row(
                children: [
                  const GocodeLogo(size: 16),
                  const Spacer(),
                  SidebarIconButton(
                    tooltip: 'Refresh',
                    icon: Icons.refresh_rounded,
                    onTap: () => ref.invalidate(sessionsProvider),
                  ),
                  if (widget.onHide != null)
                    SidebarIconButton(
                      tooltip: 'Hide sidebar',
                      icon: Icons.keyboard_double_arrow_left_rounded,
                      onTap: widget.onHide!,
                    ),
                ],
              ),
            ),
            // Actions: new session and search share one row style, so
            // their icons and labels line up.
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 8),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  SidebarRow(
                    key: SidebarKeys.newSession,
                    onTap: () => _go('/new'),
                    builder: (_) => const Row(
                      children: [
                        SizedBox(
                          width: 16,
                          child: Icon(
                            Icons.add_rounded,
                            size: 16,
                            color: GC.accentText,
                          ),
                        ),
                        SizedBox(width: 8),
                        Text('New session', style: _navStyle),
                      ],
                    ),
                  ),
                  SidebarRow(
                    key: SidebarKeys.sourceControl,
                    selected: widget.location == '/git',
                    onTap: () => _go(
                      sourceControlLocation(
                        directory: selectedProject?.directory,
                      ),
                    ),
                    builder: (_) => const Row(
                      children: [
                        SizedBox(
                          width: 16,
                          child: Icon(
                            Icons.account_tree_outlined,
                            size: 15,
                            color: GC.textBody,
                          ),
                        ),
                        SizedBox(width: 8),
                        Text('Source control', style: _navStyle),
                      ],
                    ),
                  ),
                  _SearchField(
                    controller: _search,
                    onChanged: (v) =>
                        setState(() => _query = v.trim().toLowerCase()),
                  ),
                  if (selectedProject != null)
                    Padding(
                      padding: const EdgeInsets.only(top: 4),
                      child: _SelectedProjectChip(project: selectedProject),
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 14, 11, 0),
              child: _TabSwitch(
                index: _tab,
                onChanged: (i) => setState(() => _tab = i),
                trailing: _tab == 0
                    ? SidebarIconButton(
                        key: SidebarKeys.newProject,
                        tooltip: 'New project',
                        icon: Icons.create_new_folder_outlined,
                        onTap: () => showDialog<void>(
                          context: context,
                          builder: (_) => const _NewProjectDialog(),
                        ),
                      )
                    : null,
              ),
            ),
            Expanded(
              child: _tab == 0
                  ? _ProjectsTab(
                      query: _query,
                      activeSessionID: _activeSessionID,
                      onOpenSession: (session) => _go('/session/${session.id}'),
                      onSessionAction: _act,
                      onNewSessionHere: () => _go('/new'),
                    )
                  : _ChatsTab(
                      query: _query,
                      activeSessionID: _activeSessionID,
                      onOpenSession: (session) => _go('/session/${session.id}'),
                      onSessionAction: _act,
                    ),
            ),
            const Divider(),
            _AccountButton(onNavigate: _go),
          ],
        ),
      ),
    );
  }
}

/// A compact square icon control for the sidebar's dense rows.
class SidebarIconButton extends StatelessWidget {
  const SidebarIconButton({
    super.key,
    required this.icon,
    required this.tooltip,
    required this.onTap,
  });

  final IconData icon;
  final String tooltip;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return Tooltip(
      message: tooltip,
      waitDuration: const Duration(milliseconds: 400),
      child: InkWell(
        borderRadius: BorderRadius.circular(5),
        onTap: onTap,
        child: SizedBox.square(
          dimension: _actionSize,
          child: Icon(icon, size: 15, color: GC.textDim),
        ),
      ),
    );
  }
}

/// Side of a row-level action (icon button, overflow menu).
const _actionSize = 22.0;

/// Height of every list row in the sidebar.
const _rowHeight = 28.0;

/// One dense, hoverable sidebar row. [builder] gets whether the pointer is
/// over the row, so trailing actions can stay hidden until they're wanted.
class SidebarRow extends StatefulWidget {
  const SidebarRow({
    super.key,
    required this.onTap,
    required this.builder,
    this.selected = false,
    this.tooltip,
  });

  final VoidCallback onTap;
  final Widget Function(bool hovered) builder;
  final bool selected;

  /// Shown after a long hover, e.g. the row's working directory.
  final String? tooltip;

  @override
  State<SidebarRow> createState() => _SidebarRowState();
}

class _SidebarRowState extends State<SidebarRow> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    Widget row = MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: InkWell(
        onTap: widget.onTap,
        borderRadius: BorderRadius.circular(6),
        child: Ink(
          height: _rowHeight,
          padding: const EdgeInsets.only(left: 8, right: 3),
          decoration: BoxDecoration(
            color: widget.selected ? const Color(0x14FFFFFF) : null,
            borderRadius: BorderRadius.circular(6),
          ),
          child: widget.builder(_hovered),
        ),
      ),
    );
    if (widget.tooltip != null) {
      row = Tooltip(
        message: widget.tooltip!,
        waitDuration: const Duration(milliseconds: 900),
        child: row,
      );
    }
    return row;
  }
}

/// The overflow (⋯) menu at the end of a row.
class _RowMenu extends StatelessWidget {
  const _RowMenu({
    required this.tooltip,
    required this.items,
    required this.onSelected,
  });

  final String tooltip;
  final List<(String, String)> items;
  final ValueChanged<String> onSelected;

  @override
  Widget build(BuildContext context) {
    return PopupMenuButton<String>(
      tooltip: tooltip,
      padding: EdgeInsets.zero,
      onSelected: onSelected,
      itemBuilder: (_) => [
        for (final (value, label) in items) compactMenuItem(value, label),
      ],
      child: const SizedBox.square(
        dimension: _actionSize,
        child: Icon(Icons.more_horiz_rounded, size: 15, color: GC.textDim),
      ),
    );
  }
}

/// A small group label inside the lists (Today, Yesterday, …).
class _GroupLabel extends StatelessWidget {
  const _GroupLabel(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    return Text(
      text,
      style: const TextStyle(
        fontFamily: GC.sans,
        fontSize: 11.5,
        fontWeight: FontWeight.w500,
        color: GC.textFaint,
      ),
    );
  }
}

/// Search, styled as a sidebar row: no box until focused.
class _SearchField extends StatelessWidget {
  const _SearchField({required this.controller, required this.onChanged});

  final TextEditingController controller;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) {
    OutlineInputBorder border(Color color) => OutlineInputBorder(
      borderRadius: BorderRadius.circular(6),
      borderSide: BorderSide(color: color),
    );
    return SizedBox(
      height: _rowHeight,
      child: TextField(
        controller: controller,
        onChanged: onChanged,
        textAlignVertical: TextAlignVertical.center,
        style: _navStyle.copyWith(fontWeight: FontWeight.w400),
        decoration: InputDecoration(
          hintText: 'Search',
          hintStyle: _navStyle.copyWith(color: GC.textDim),
          prefixIcon: const Padding(
            padding: EdgeInsets.only(left: 8, right: 4),
            child: SizedBox(
              width: 16,
              child: Icon(Icons.search_rounded, size: 15, color: GC.textDim),
            ),
          ),
          prefixIconConstraints: const BoxConstraints(),
          contentPadding: const EdgeInsets.only(right: 8),
          filled: true,
          fillColor: Colors.transparent,
          hoverColor: const Color(0x0AFFFFFF),
          border: border(Colors.transparent),
          enabledBorder: border(Colors.transparent),
          focusedBorder: border(GC.borderStrong),
        ),
      ),
    );
  }
}

/// Label style for the action rows at the top.
const _navStyle = TextStyle(
  fontFamily: GC.sans,
  fontSize: 13,
  fontWeight: FontWeight.w500,
  color: GC.textHi,
);

/// Projects / Chats as two quiet text tabs, with an optional action at the
/// far end of the row.
class _TabSwitch extends StatelessWidget {
  const _TabSwitch({
    required this.index,
    required this.onChanged,
    this.trailing,
  });

  final int index;
  final ValueChanged<int> onChanged;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    Widget tab(Key key, int i, String label) {
      final selected = index == i;
      return InkWell(
        key: key,
        borderRadius: BorderRadius.circular(4),
        onTap: () => onChanged(i),
        child: Padding(
          padding: const EdgeInsets.symmetric(vertical: 4),
          child: AnimatedDefaultTextStyle(
            duration: GC.dur,
            curve: GC.ease,
            style: TextStyle(
              fontFamily: GC.sans,
              fontSize: 12,
              fontWeight: FontWeight.w600,
              color: selected ? GC.textHi : GC.textFaint,
            ),
            child: Text(label),
          ),
        ),
      );
    }

    return SizedBox(
      height: 24,
      child: Row(
        children: [
          tab(SidebarKeys.projectsTab, 0, 'Projects'),
          const SizedBox(width: 14),
          tab(SidebarKeys.chatsTab, 1, 'Chats'),
          const Spacer(),
          ?trailing,
        ],
      ),
    );
  }
}

/// The sticky reminder that new chats will attach to [project], with a way
/// to back out of that without hunting for the project row again.
class _SelectedProjectChip extends ConsumerWidget {
  const _SelectedProjectChip({required this.project});

  final Project project;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return Container(
      height: 24,
      padding: const EdgeInsets.only(left: 10, right: 2),
      decoration: BoxDecoration(
        color: const Color(0x14E8862D),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Row(
        children: [
          const Icon(Icons.folder_rounded, size: 12, color: GC.accentText),
          const SizedBox(width: 6),
          Expanded(
            child: Text(
              'New chats in ${project.name}',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(
                fontFamily: GC.sans,
                fontSize: 11.5,
                fontWeight: FontWeight.w500,
                color: GC.accentText,
              ),
            ),
          ),
          InkWell(
            borderRadius: BorderRadius.circular(4),
            onTap: () => ref.read(selectedProjectProvider.notifier).clear(),
            child: const SizedBox.square(
              dimension: 20,
              child: Icon(Icons.close_rounded, size: 12, color: GC.accentText),
            ),
          ),
        ],
      ),
    );
  }
}

class _ProjectsTab extends ConsumerWidget {
  const _ProjectsTab({
    required this.query,
    required this.activeSessionID,
    required this.onOpenSession,
    required this.onSessionAction,
    required this.onNewSessionHere,
  });

  final String query;
  final String? activeSessionID;
  final ValueChanged<Session> onOpenSession;
  final Future<void> Function(String action, Session session) onSessionAction;
  final VoidCallback onNewSessionHere;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final projects = ref.watch(projectsProvider);
    final selected = ref.watch(selectedProjectProvider);
    final sessionsAsync = ref.watch(sessionsProvider);

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Expanded(
          child: sessionsAsync.when(
            loading: () => const Center(
              child: SizedBox.square(
                dimension: 18,
                child: CircularProgressIndicator(strokeWidth: 2),
              ),
            ),
            error: (e, _) => Padding(
              padding: const EdgeInsets.all(12),
              child: ErrorPanel(
                message: '$e',
                onRetry: () => ref.invalidate(sessionsProvider),
              ),
            ),
            data: (sessions) {
              final grouped = groupByProject(projects, sessions);
              final visible = query.isEmpty
                  ? grouped.projects
                  : grouped.projects
                        .where(
                          (g) =>
                              g.project.name.toLowerCase().contains(query) ||
                              g.project.directory.toLowerCase().contains(
                                query,
                              ) ||
                              g.sessions.any(
                                (s) =>
                                    s.title.toLowerCase().contains(query) ||
                                    s.directory.toLowerCase().contains(query),
                              ),
                        )
                        .toList();
              if (visible.isEmpty) {
                return Padding(
                  padding: const EdgeInsets.fromLTRB(20, 20, 20, 0),
                  child: Text(
                    projects.isEmpty
                        ? 'No projects yet. Create one to group chats by '
                              'work folder.'
                        : 'No projects match.',
                    textAlign: TextAlign.center,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                );
              }
              return ListView(
                padding: const EdgeInsets.fromLTRB(8, 8, 8, 8),
                children: [
                  for (final group in visible)
                    _ProjectTile(
                      group: group,
                      selected: group.project.id == selected,
                      query: query,
                      activeSessionID: activeSessionID,
                      onOpenSession: onOpenSession,
                      onSessionAction: onSessionAction,
                      onNewSessionHere: onNewSessionHere,
                    ),
                ],
              );
            },
          ),
        ),
      ],
    );
  }
}

class _ProjectTile extends ConsumerWidget {
  const _ProjectTile({
    required this.group,
    required this.selected,
    required this.query,
    required this.activeSessionID,
    required this.onOpenSession,
    required this.onSessionAction,
    required this.onNewSessionHere,
  });

  final ProjectGroup group;
  final bool selected;
  final String query;
  final String? activeSessionID;
  final ValueChanged<Session> onOpenSession;
  final Future<void> Function(String action, Session session) onSessionAction;
  final VoidCallback onNewSessionHere;

  Future<void> _rename(BuildContext context, WidgetRef ref) async {
    final controller = TextEditingController(text: group.project.name);
    final name = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Rename project'),
        content: SizedBox(
          width: 420,
          child: TextField(
            controller: controller,
            autofocus: true,
            onSubmitted: (value) => Navigator.pop(context, value),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, controller.text),
            child: const Text('Save'),
          ),
        ],
      ),
    );
    if (name == null || name.trim().isEmpty) return;
    await ref.read(projectsProvider.notifier).rename(group.project.id, name);
  }

  Future<void> _delete(BuildContext context, WidgetRef ref) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Remove project?'),
        content: Text(
          '"${group.project.name}" will no longer group its chats — they '
          'stay in Chats. Nothing is deleted.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            style: FilledButton.styleFrom(
              backgroundColor: GC.down,
              foregroundColor: GC.textHi,
            ),
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Remove'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;
    if (selected) ref.read(selectedProjectProvider.notifier).clear();
    await ref.read(projectsProvider.notifier).delete(group.project.id);
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final project = group.project;
    final activity = ref.watch(sessionActivityProvider);
    final busy = group.sessions.any((s) => activity[s.id]?.busy ?? false);
    final sessions = query.isEmpty
        ? group.sessions
        : group.sessions
              .where(
                (s) =>
                    s.title.toLowerCase().contains(query) ||
                    s.directory.toLowerCase().contains(query),
              )
              .toList();

    final count = group.sessions.length;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        SidebarRow(
          tooltip: project.directory,
          selected: selected,
          onTap: () {
            final notifier = ref.read(selectedProjectProvider.notifier);
            if (selected) {
              notifier.clear();
            } else {
              notifier.select(project.id);
            }
          },
          builder: (hovered) => Row(
            children: [
              AnimatedRotation(
                turns: selected ? 0.25 : 0,
                duration: GC.dur,
                curve: GC.ease,
                child: const Icon(
                  Icons.chevron_right_rounded,
                  size: 14,
                  color: GC.textFaint,
                ),
              ),
              const SizedBox(width: 4),
              Icon(
                selected ? Icons.folder_open_rounded : Icons.folder_outlined,
                size: 14,
                color: selected ? GC.accentText : GC.textDim,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  project.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    fontFamily: GC.sans,
                    fontSize: 13,
                    fontWeight: selected ? FontWeight.w600 : FontWeight.w500,
                    color: selected ? GC.textHi : GC.textBody,
                  ),
                ),
              ),
              if (busy) ...[
                const LiveDot(busy: true, size: 6),
                const SizedBox(width: 6),
              ],
              if (hovered) ...[
                SidebarIconButton(
                  tooltip: 'New chat in ${project.name}',
                  icon: Icons.add_rounded,
                  onTap: () {
                    ref
                        .read(selectedProjectProvider.notifier)
                        .select(project.id);
                    onNewSessionHere();
                  },
                ),
                _RowMenu(
                  tooltip: 'Project actions',
                  items: const [('rename', 'Rename'), ('delete', 'Remove')],
                  onSelected: (action) => switch (action) {
                    'rename' => _rename(context, ref),
                    'delete' => _delete(context, ref),
                    _ => null,
                  },
                ),
              ] else if (count > 0)
                Padding(
                  padding: const EdgeInsets.only(right: 6),
                  child: Text('$count', style: _metaStyle),
                ),
            ],
          ),
        ),
        AnimatedCrossFade(
          duration: GC.dur,
          sizeCurve: GC.ease,
          crossFadeState: selected
              ? CrossFadeState.showSecond
              : CrossFadeState.showFirst,
          firstChild: const SizedBox(width: double.infinity),
          secondChild: Container(
            margin: const EdgeInsets.only(left: 15, top: 2, bottom: 6),
            padding: const EdgeInsets.only(left: 6),
            decoration: const BoxDecoration(
              border: Border(left: BorderSide(color: GC.border)),
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (sessions.isEmpty)
                  Padding(
                    padding: const EdgeInsets.fromLTRB(8, 4, 8, 4),
                    child: Text(
                      query.isEmpty ? 'No chats yet.' : 'No chats match.',
                      style: _metaStyle,
                    ),
                  )
                else
                  for (final session in sessions)
                    _SessionItem(
                      session: session,
                      active: session.id == activeSessionID,
                      age: shortAge(session.timeUpdated, DateTime.now()),
                      onOpen: () => onOpenSession(session),
                      onAction: (action) => onSessionAction(action, session),
                    ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

class _NewProjectDialog extends ConsumerStatefulWidget {
  const _NewProjectDialog();

  @override
  ConsumerState<_NewProjectDialog> createState() => _NewProjectDialogState();
}

class _NewProjectDialogState extends ConsumerState<_NewProjectDialog> {
  final _name = TextEditingController();
  final _directory = TextEditingController();

  @override
  void initState() {
    super.initState();
    _directory.addListener(_rebuild);
  }

  void _rebuild() => setState(() {});

  @override
  void dispose() {
    _name.dispose();
    _directory.dispose();
    super.dispose();
  }

  Future<void> _pickDirectory() async {
    final result = await FilePicker.getDirectoryPath();
    if (result == null) return;
    _directory.text = result;
    if (_name.text.trim().isEmpty) _name.text = nameFromDirectory(result);
  }

  Future<void> _create() async {
    final directory = _directory.text.trim();
    if (directory.isEmpty) return;
    final project = await ref
        .read(projectsProvider.notifier)
        .create(name: _name.text, directory: directory);
    ref.read(selectedProjectProvider.notifier).select(project.id);
    if (mounted) Navigator.pop(context);
  }

  @override
  Widget build(BuildContext context) {
    final canCreate = _directory.text.trim().isNotEmpty;
    return AlertDialog(
      title: const Text('New project'),
      content: SizedBox(
        width: 420,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Caption('Name'),
            const SizedBox(height: 8),
            TextField(
              controller: _name,
              autofocus: true,
              decoration: const InputDecoration(
                hintText: 'Defaults to the folder name',
              ),
            ),
            const SizedBox(height: 18),
            const Caption('Work folder'),
            const SizedBox(height: 8),
            Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _directory,
                    style: GC.code.copyWith(fontSize: 13.5, color: GC.textHi),
                    decoration: const InputDecoration(
                      hintText: '/path/to/project',
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                OutlinedButton(
                  onPressed: _pickDirectory,
                  child: const Text('Browse'),
                ),
              ],
            ),
            const SizedBox(height: 6),
            Text(
              'Chats started in this folder join the project automatically.',
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: canCreate ? _create : null,
          child: const Text('Create'),
        ),
      ],
    );
  }
}

class _ChatsTab extends ConsumerWidget {
  const _ChatsTab({
    required this.query,
    required this.activeSessionID,
    required this.onOpenSession,
    required this.onSessionAction,
  });

  final String query;
  final String? activeSessionID;
  final ValueChanged<Session> onOpenSession;
  final Future<void> Function(String action, Session session) onSessionAction;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final projects = ref.watch(projectsProvider);
    final sessionsAsync = ref.watch(sessionsProvider);
    return sessionsAsync.when(
      loading: () => const Center(
        child: SizedBox.square(
          dimension: 18,
          child: CircularProgressIndicator(strokeWidth: 2),
        ),
      ),
      error: (e, _) => Padding(
        padding: const EdgeInsets.all(12),
        child: ErrorPanel(
          message: '$e',
          onRetry: () => ref.invalidate(sessionsProvider),
        ),
      ),
      data: (sessions) {
        final grouped = groupByProject(projects, sessions);
        return _History(
          sessions: grouped.unassigned,
          query: query,
          activeID: activeSessionID,
          onOpen: onOpenSession,
          onAction: onSessionAction,
        );
      },
    );
  }
}

class _History extends StatelessWidget {
  const _History({
    required this.sessions,
    required this.query,
    required this.activeID,
    required this.onOpen,
    required this.onAction,
  });

  final List<Session> sessions;
  final String query;
  final String? activeID;
  final ValueChanged<Session> onOpen;
  final Future<void> Function(String action, Session session) onAction;

  @override
  Widget build(BuildContext context) {
    final now = DateTime.now();
    // Subagent sessions are children of another; they'd crowd the history.
    final visible =
        sessions
            .where(
              (s) =>
                  !s.isSubagent &&
                  (query.isEmpty ||
                      s.title.toLowerCase().contains(query) ||
                      s.directory.toLowerCase().contains(query)),
            )
            .toList()
          ..sort((a, b) => b.timeUpdated.compareTo(a.timeUpdated));
    if (visible.isEmpty) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(20, 24, 20, 0),
        child: Text(
          query.isEmpty ? 'No chats yet.' : 'No chats match.',
          textAlign: TextAlign.center,
          style: Theme.of(context).textTheme.bodySmall,
        ),
      );
    }
    return ListView(
      padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
      children: [
        for (final group in groupSessions(visible, now)) ...[
          Padding(
            padding: const EdgeInsets.fromLTRB(8, 14, 8, 4),
            child: _GroupLabel(group.label),
          ),
          for (final session in group.sessions)
            _SessionItem(
              session: session,
              active: session.id == activeID,
              age: shortAge(session.timeUpdated, now),
              onOpen: () => onOpen(session),
              onAction: (action) => onAction(action, session),
            ),
        ],
      ],
    );
  }
}

/// Faint trailing metadata: ages, counts, empty-list notes.
const _metaStyle = TextStyle(
  fontFamily: GC.sans,
  fontSize: 11,
  color: GC.textFaint,
);

class _SessionItem extends ConsumerWidget {
  const _SessionItem({
    required this.session,
    required this.active,
    required this.age,
    required this.onOpen,
    required this.onAction,
  });

  final Session session;
  final bool active;
  final String age;
  final VoidCallback onOpen;
  final ValueChanged<String> onAction;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final title = session.title.isEmpty ? 'Untitled' : session.title;
    final activity =
        ref.watch(sessionActivityProvider)[session.id] ?? SessionActivity.idle;
    final showLiveStatus = activity.busy || activity.justFinished;
    return SidebarRow(
      tooltip: session.directory,
      selected: active,
      onTap: onOpen,
      builder: (hovered) => Row(
        children: [
          Expanded(
            child: Text(
              title,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontFamily: GC.sans,
                fontSize: 13,
                fontWeight: active ? FontWeight.w500 : FontWeight.w400,
                color: active ? GC.textHi : GC.textBody,
              ),
            ),
          ),
          if (hovered || active)
            _RowMenu(
              tooltip: 'Session actions',
              items: const [
                ('rename', 'Rename'),
                ('fork', 'Fork'),
                ('delete', 'Delete'),
              ],
              onSelected: onAction,
            )
          // Trailing, so titles stay aligned: live status beats age.
          else if (showLiveStatus)
            SizedBox.square(
              dimension: _actionSize,
              child: Center(
                child: LiveDot(
                  busy: activity.busy,
                  justFinished: activity.justFinished,
                  size: 6,
                ),
              ),
            )
          else
            Padding(
              padding: const EdgeInsets.only(left: 6, right: 5),
              child: Text(age, style: _metaStyle),
            ),
        ],
      ),
    );
  }
}

/// The account button at the bottom: who is signed in, and the menu for the
/// account pages, the app's settings, and signing in or out.
class _AccountButton extends ConsumerWidget {
  const _AccountButton({required this.onNavigate});

  final ValueChanged<String> onNavigate;

  Future<void> _signOut(BuildContext context, WidgetRef ref) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Sign out of gocoder.org?'),
        content: const Text(
          "This machine's API key is revoked, and settings sync stops until "
          'you sign in again.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Sign out'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;
    try {
      await signOutOfAccount(ref);
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(accountErrorText(e))));
      }
    }
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final account = ref.watch(accountProvider);
    final info = account.value;
    final signedIn = info?.signedIn ?? false;
    final expired = info?.expired ?? false;

    PopupMenuItem<String> item(String value, IconData icon, String label) =>
        compactMenuItem(value, label, icon: icon);

    final String title;
    if (signedIn) {
      title = info!.name;
    } else {
      title = account.isLoading ? '…' : 'Not signed in';
    }

    return Padding(
      padding: const EdgeInsets.all(6),
      child: PopupMenuButton<String>(
        key: SidebarKeys.accountMenu,
        tooltip: 'Account and settings',
        position: PopupMenuPosition.over,
        constraints: const BoxConstraints(minWidth: sidebarWidth - 12),
        onSelected: (value) {
          if (value == 'signout') {
            _signOut(context, ref);
          } else {
            onNavigate(value);
          }
        },
        itemBuilder: (_) => [
          if (signedIn)
            PopupMenuItem<String>(
              enabled: false,
              height: 28,
              child: Text(
                expired ? '${info!.email} · session expired' : info!.email,
                style: theme.textTheme.bodySmall?.copyWith(fontSize: 11.5),
              ),
            ),
          item('/account/profile', Icons.person_outline_rounded, 'Profile'),
          item(
            '/account/settings',
            Icons.manage_accounts_outlined,
            'User settings',
          ),
          item('/settings', Icons.tune_rounded, 'Settings'),
          item('/account/usage', Icons.insights_rounded, 'Usage'),
          item(
            '/account/invite',
            Icons.card_giftcard_rounded,
            'Invite a friend',
          ),
          const PopupMenuDivider(),
          if (signedIn)
            item('signout', Icons.logout_rounded, 'Sign out')
          else
            item('/account/profile', Icons.login_rounded, 'Sign in'),
        ],
        child: SizedBox(
          height: 32,
          child: Row(
            children: [
              const SizedBox(width: 6),
              AccountAvatar(
                size: 22,
                initials: signedIn ? info!.initials : null,
                badge: expired ? GC.warn : null,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  title,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: const TextStyle(
                    fontFamily: GC.sans,
                    fontSize: 13,
                    fontWeight: FontWeight.w500,
                    color: GC.textHi,
                  ),
                ),
              ),
              if (expired)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: Text(
                    'Sign in again',
                    style: TextStyle(
                      fontFamily: GC.sans,
                      fontSize: 11,
                      color: GC.warn,
                    ),
                  ),
                ),
              const SizedBox(width: 4),
              const Icon(
                Icons.unfold_more_rounded,
                size: 15,
                color: GC.textFaint,
              ),
              const SizedBox(width: 6),
            ],
          ),
        ),
      ),
    );
  }
}

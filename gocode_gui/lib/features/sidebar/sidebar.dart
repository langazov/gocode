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
import '../home/providers.dart';
import 'history.dart';
import 'project_grouping.dart';
import 'projects.dart';

/// Width of the docked sidebar (and the drawer on narrow windows).
const sidebarWidth = 284.0;

/// Keys tests reach sidebar controls by.
abstract final class SidebarKeys {
  static const newSession = ValueKey('sidebar-new-session');
  static const accountMenu = ValueKey('sidebar-account-menu');
  static const projectsTab = ValueKey('sidebar-projects-tab');
  static const chatsTab = ValueKey('sidebar-chats-tab');
  static const newProject = ValueKey('sidebar-new-project');
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

class _SidebarState extends ConsumerState<Sidebar>
    with SingleTickerProviderStateMixin {
  final _search = TextEditingController();
  String _query = '';
  late final _tabs = TabController(
    length: 2,
    vsync: this,
    // Most installs start with no projects yet; defaulting here means a
    // returning user's existing chats are never hidden behind an empty tab.
    initialIndex: 1,
  );

  @override
  void dispose() {
    _search.dispose();
    _tabs.dispose();
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
    final client = ref.read(apiClientProvider);
    if (client == null) return;
    try {
      switch (action) {
        case 'rename':
          final title = await _promptTitle(session.title);
          if (title == null || title.trim().isEmpty) return;
          await client.renameSession(session.id, title.trim());
        case 'fork':
          await client.forkSession(session.id);
        case 'delete':
          if (await _confirmDelete(session.title) != true) return;
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
              padding: const EdgeInsets.fromLTRB(18, 14, 6, 10),
              child: Row(
                children: [
                  const GocodeLogo(size: 20),
                  const Spacer(),
                  IconButton(
                    tooltip: 'Refresh',
                    visualDensity: VisualDensity.compact,
                    icon: const Icon(Icons.refresh_rounded, size: 18),
                    onPressed: () => ref.invalidate(sessionsProvider),
                  ),
                  if (widget.onHide != null)
                    IconButton(
                      tooltip: 'Hide sidebar',
                      visualDensity: VisualDensity.compact,
                      icon: const Icon(
                        Icons.keyboard_double_arrow_left_rounded,
                        size: 18,
                      ),
                      onPressed: widget.onHide,
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12),
              child: FilledButton.icon(
                key: SidebarKeys.newSession,
                onPressed: () => _go('/new'),
                icon: const Icon(Icons.add_rounded, size: 18),
                label: const Text('New session'),
              ),
            ),
            if (selectedProject != null)
              Padding(
                padding: const EdgeInsets.fromLTRB(12, 8, 12, 0),
                child: _SelectedProjectChip(project: selectedProject),
              ),
            const SizedBox(height: 10),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12),
              child: TextField(
                controller: _search,
                onChanged: (v) =>
                    setState(() => _query = v.trim().toLowerCase()),
                style: const TextStyle(
                  fontFamily: GC.sans,
                  fontSize: 13.5,
                  color: GC.textHi,
                ),
                decoration: const InputDecoration(
                  hintText: 'Search',
                  prefixIcon: Icon(Icons.search, size: 16),
                  contentPadding: EdgeInsets.symmetric(
                    horizontal: 12,
                    vertical: 10,
                  ),
                ),
              ),
            ),
            const SizedBox(height: 4),
            TabBar(
              controller: _tabs,
              tabs: [
                Tab(key: SidebarKeys.projectsTab, text: 'Projects'),
                Tab(key: SidebarKeys.chatsTab, text: 'Chats'),
              ],
              labelColor: GC.accentText,
              unselectedLabelColor: GC.textDim,
              indicatorColor: GC.accent,
              indicatorSize: TabBarIndicatorSize.label,
              labelStyle: const TextStyle(
                fontFamily: GC.sans,
                fontSize: 13,
                fontWeight: FontWeight.w600,
              ),
              unselectedLabelStyle: const TextStyle(
                fontFamily: GC.sans,
                fontSize: 13,
                fontWeight: FontWeight.w500,
              ),
              dividerColor: GC.border,
            ),
            Expanded(
              child: TabBarView(
                controller: _tabs,
                children: [
                  _ProjectsTab(
                    query: _query,
                    activeSessionID: _activeSessionID,
                    onOpenSession: (session) => _go('/session/${session.id}'),
                    onSessionAction: _act,
                    onNewSessionHere: () => _go('/new'),
                  ),
                  _ChatsTab(
                    query: _query,
                    activeSessionID: _activeSessionID,
                    onOpenSession: (session) => _go('/session/${session.id}'),
                    onSessionAction: _act,
                  ),
                ],
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

/// The sticky reminder that new chats will attach to [project], with a way
/// to back out of that without hunting for the project row again.
class _SelectedProjectChip extends ConsumerWidget {
  const _SelectedProjectChip({required this.project});

  final Project project;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return Container(
      padding: const EdgeInsets.fromLTRB(10, 6, 6, 6),
      decoration: BoxDecoration(
        color: const Color(0x1FE8862D),
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: GC.borderAccent),
      ),
      child: Row(
        children: [
          const Icon(Icons.folder_rounded, size: 13, color: GC.accentText),
          const SizedBox(width: 6),
          Expanded(
            child: Text(
              'New chats → ${project.name}',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(
                fontFamily: GC.sans,
                fontSize: 11.5,
                fontWeight: FontWeight.w600,
                color: GC.accentText,
              ),
            ),
          ),
          InkWell(
            borderRadius: BorderRadius.circular(999),
            onTap: () => ref.read(selectedProjectProvider.notifier).clear(),
            child: const Padding(
              padding: EdgeInsets.all(3),
              child: Icon(Icons.close_rounded, size: 13, color: GC.accentText),
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
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 8, 4),
          child: Row(
            children: [
              const Expanded(child: Caption('Work folders')),
              TextButton.icon(
                key: SidebarKeys.newProject,
                onPressed: () => showDialog<void>(
                  context: context,
                  builder: (_) => const _NewProjectDialog(),
                ),
                style: TextButton.styleFrom(
                  padding: const EdgeInsets.symmetric(horizontal: 8),
                  visualDensity: VisualDensity.compact,
                ),
                icon: const Icon(Icons.create_new_folder_outlined, size: 15),
                label: const Text('New'),
              ),
            ],
          ),
        ),
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
                padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
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

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Tooltip(
          message: project.directory,
          waitDuration: const Duration(milliseconds: 700),
          child: Material(
            type: MaterialType.transparency,
            child: InkWell(
              borderRadius: BorderRadius.circular(10),
              onTap: () {
                final notifier = ref.read(selectedProjectProvider.notifier);
                if (selected) {
                  notifier.clear();
                } else {
                  notifier.select(project.id);
                }
              },
              child: Ink(
                decoration: BoxDecoration(
                  color: selected ? const Color(0x17FFFFFF) : null,
                  borderRadius: BorderRadius.circular(10),
                ),
                padding: const EdgeInsets.fromLTRB(8, 8, 2, 8),
                child: Row(
                  children: [
                    Icon(
                      selected
                          ? Icons.folder_open_rounded
                          : Icons.folder_outlined,
                      size: 17,
                      color: selected ? GC.accentText : GC.textDim,
                    ),
                    const SizedBox(width: 8),
                    if (busy) ...[
                      const LiveDot(busy: true, size: 6),
                      const SizedBox(width: 7),
                    ],
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            project.name,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: TextStyle(
                              fontFamily: GC.sans,
                              fontSize: 13.5,
                              fontWeight: selected
                                  ? FontWeight.w600
                                  : FontWeight.w500,
                              color: selected ? GC.textHi : GC.textBody,
                            ),
                          ),
                          Text(
                            group.sessions.isEmpty
                                ? 'no chats'
                                : '${group.sessions.length} chat'
                                      '${group.sessions.length == 1 ? '' : 's'}',
                            style: const TextStyle(
                              fontFamily: GC.sans,
                              fontSize: 11,
                              color: GC.textFaint,
                            ),
                          ),
                        ],
                      ),
                    ),
                    Tooltip(
                      message: 'New chat in ${project.name}',
                      child: InkWell(
                        borderRadius: BorderRadius.circular(999),
                        onTap: () {
                          ref
                              .read(selectedProjectProvider.notifier)
                              .select(project.id);
                          onNewSessionHere();
                        },
                        child: const Padding(
                          padding: EdgeInsets.symmetric(
                            horizontal: 6,
                            vertical: 6,
                          ),
                          child: Icon(
                            Icons.add_circle_outline_rounded,
                            size: 16,
                            color: GC.textDim,
                          ),
                        ),
                      ),
                    ),
                    PopupMenuButton<String>(
                      tooltip: 'Project actions',
                      onSelected: (action) => switch (action) {
                        'rename' => _rename(context, ref),
                        'delete' => _delete(context, ref),
                        _ => null,
                      },
                      itemBuilder: (_) => const [
                        PopupMenuItem(value: 'rename', child: Text('Rename')),
                        PopupMenuItem(value: 'delete', child: Text('Remove')),
                      ],
                      child: const Padding(
                        padding: EdgeInsets.symmetric(
                          horizontal: 6,
                          vertical: 6,
                        ),
                        child: Icon(
                          Icons.more_horiz_rounded,
                          size: 16,
                          color: GC.textDim,
                        ),
                      ),
                    ),
                    AnimatedRotation(
                      turns: selected ? 0.5 : 0,
                      duration: GC.dur,
                      curve: GC.ease,
                      child: const Padding(
                        padding: EdgeInsets.only(right: 6),
                        child: Icon(
                          Icons.expand_more_rounded,
                          size: 18,
                          color: GC.textDim,
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ),
        ),
        AnimatedCrossFade(
          duration: GC.dur,
          sizeCurve: GC.ease,
          crossFadeState: selected
              ? CrossFadeState.showSecond
              : CrossFadeState.showFirst,
          firstChild: const SizedBox(width: double.infinity),
          secondChild: Padding(
            padding: const EdgeInsets.only(left: 12, bottom: 6),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (sessions.isEmpty)
                  Padding(
                    padding: const EdgeInsets.fromLTRB(10, 4, 10, 6),
                    child: Text(
                      query.isEmpty ? 'No chats yet.' : 'No chats match.',
                      style: Theme.of(context).textTheme.bodySmall,
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
      padding: const EdgeInsets.fromLTRB(8, 6, 8, 8),
      children: [
        for (final group in groupSessions(visible, now)) ...[
          Padding(
            padding: const EdgeInsets.fromLTRB(10, 14, 10, 6),
            child: Caption(group.label),
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

class _SessionItem extends ConsumerStatefulWidget {
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
  ConsumerState<_SessionItem> createState() => _SessionItemState();
}

class _SessionItemState extends ConsumerState<_SessionItem> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    final active = widget.active;
    final title = widget.session.title.isEmpty
        ? 'Untitled'
        : widget.session.title;
    final activity =
        ref.watch(sessionActivityProvider)[widget.session.id] ??
        SessionActivity.idle;
    final showLiveStatus = activity.busy || activity.justFinished;
    return MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: Tooltip(
        message: widget.session.directory,
        waitDuration: const Duration(milliseconds: 700),
        child: Material(
          type: MaterialType.transparency,
          child: InkWell(
            onTap: widget.onOpen,
            borderRadius: BorderRadius.circular(10),
            child: Ink(
              decoration: BoxDecoration(
                color: active ? const Color(0x17FFFFFF) : null,
                borderRadius: BorderRadius.circular(10),
              ),
              padding: const EdgeInsets.fromLTRB(10, 0, 2, 0),
              child: SizedBox(
                height: 36,
                child: Row(
                  children: [
                    if (active)
                      Container(
                        width: 3,
                        height: 14,
                        margin: const EdgeInsets.only(right: 8),
                        decoration: BoxDecoration(
                          color: GC.accent,
                          borderRadius: BorderRadius.circular(2),
                        ),
                      ),
                    if (showLiveStatus) ...[
                      LiveDot(
                        busy: activity.busy,
                        justFinished: activity.justFinished,
                        size: 6,
                      ),
                      const SizedBox(width: 8),
                    ],
                    Expanded(
                      child: Text(
                        title,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                          fontFamily: GC.sans,
                          fontSize: 13.5,
                          fontWeight: active
                              ? FontWeight.w600
                              : FontWeight.w400,
                          color: active ? GC.textHi : GC.textBody,
                        ),
                      ),
                    ),
                    if (_hovered || active)
                      PopupMenuButton<String>(
                        tooltip: 'Session actions',
                        onSelected: widget.onAction,
                        itemBuilder: (_) => const [
                          PopupMenuItem(value: 'rename', child: Text('Rename')),
                          PopupMenuItem(value: 'fork', child: Text('Fork')),
                          PopupMenuItem(value: 'delete', child: Text('Delete')),
                        ],
                        child: const Padding(
                          padding: EdgeInsets.symmetric(
                            horizontal: 8,
                            vertical: 6,
                          ),
                          child: Icon(
                            Icons.more_horiz_rounded,
                            size: 16,
                            color: GC.textDim,
                          ),
                        ),
                      )
                    else
                      Padding(
                        padding: const EdgeInsets.only(right: 10),
                        child: Text(
                          widget.age,
                          style: const TextStyle(
                            fontFamily: GC.sans,
                            fontSize: 11.5,
                            color: GC.textFaint,
                          ),
                        ),
                      ),
                  ],
                ),
              ),
            ),
          ),
        ),
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
        PopupMenuItem<String>(
          value: value,
          height: 40,
          child: Row(
            children: [
              Icon(icon, size: 18, color: GC.textDim),
              const SizedBox(width: 12),
              Text(label),
            ],
          ),
        );

    final String title;
    final String subtitle;
    if (signedIn) {
      title = info!.name;
      subtitle = expired ? 'Sign in again' : info.email;
    } else {
      title = account.isLoading ? '…' : 'Not signed in';
      subtitle = 'Sign in to gocoder.org';
    }

    return Padding(
      padding: const EdgeInsets.all(8),
      child: PopupMenuButton<String>(
        key: SidebarKeys.accountMenu,
        tooltip: 'Account and settings',
        position: PopupMenuPosition.over,
        constraints: const BoxConstraints(minWidth: sidebarWidth - 16),
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
              height: 34,
              child: Text(info!.email, style: theme.textTheme.bodySmall),
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
        child: Padding(
          padding: const EdgeInsets.all(8),
          child: Row(
            children: [
              AccountAvatar(
                initials: signedIn ? info!.initials : null,
                badge: expired ? GC.warn : null,
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall,
                    ),
                    Text(
                      subtitle,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
              const Icon(
                Icons.unfold_more_rounded,
                size: 18,
                color: GC.textDim,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

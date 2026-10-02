import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/connection/controller.dart';
import '../../shared/widgets/glass.dart';
import '../sidebar/projects.dart';
import 'changes_view.dart';
import 'git_state.dart';
import 'history_view.dart';

/// Source Control: the working tree (stage, commit, branches, remotes,
/// stashes, conflicts) and the commit history, for one folder.
///
/// The folder is [directory] when the route names one (a session's
/// "Source control" button), else the sidebar's selected project, else the
/// server's own working directory. The header's folder menu switches
/// between them.
class GitScreen extends ConsumerStatefulWidget {
  const GitScreen({super.key, this.directory});

  final String? directory;

  @override
  ConsumerState<GitScreen> createState() => _GitScreenState();
}

class _GitScreenState extends ConsumerState<GitScreen> {
  int _tab = 0;

  /// The user's pick from the folder menu; '' means the server's folder.
  String? _picked;

  @override
  void didUpdateWidget(GitScreen old) {
    super.didUpdateWidget(old);
    if (old.directory != widget.directory) _picked = null;
  }

  String? _directory(List<Project> projects) {
    if (_picked != null) return _picked!.isEmpty ? null : _picked;
    if (widget.directory case final dir? when dir.isNotEmpty) return dir;
    return findProject(projects, ref.watch(selectedProjectProvider))?.directory;
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final client = ref.watch(apiClientProvider);
    final projects = ref.watch(projectsProvider);
    final directory = _directory(projects);
    // Providers can't change mid-build; hand the folder over right after.
    if (directory != ref.read(gitDirectoryProvider)) {
      Future.microtask(() {
        if (mounted) ref.read(gitDirectoryProvider.notifier).set(directory);
      });
    }

    return Scaffold(
      extendBodyBehindAppBar: true,
      appBar: PillHeader(
        leading: const HeaderBackButton(),
        title: Row(
          children: [
            Text('Source Control', style: theme.textTheme.titleSmall),
            const SizedBox(width: 10),
            Flexible(
              child: _FolderMenu(
                directory: directory,
                projects: projects,
                onPick: (dir) => setState(() => _picked = dir ?? ''),
              ),
            ),
          ],
        ),
        actions: [
          if (client != null)
            Padding(
              padding: const EdgeInsets.only(right: 4),
              child: SegmentedButton<int>(
                style: const ButtonStyle(visualDensity: VisualDensity.compact),
                showSelectedIcon: false,
                segments: const [
                  ButtonSegment(value: 0, label: Text('Changes')),
                  ButtonSegment(value: 1, label: Text('History')),
                ],
                selected: {_tab},
                onSelectionChanged: (s) => setState(() => _tab = s.first),
              ),
            ),
        ],
      ),
      body: Builder(
        builder: (context) => Padding(
          padding: EdgeInsets.fromLTRB(
            14,
            MediaQuery.paddingOf(context).top + 8,
            14,
            14,
          ),
          child: Align(
            alignment: Alignment.topCenter,
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 1240),
              child: Material(
                color: GC.surface1,
                clipBehavior: Clip.antiAlias,
                shape: RoundedRectangleBorder(
                  borderRadius: BorderRadius.circular(GC.rCard),
                  side: const BorderSide(color: GC.border),
                ),
                child: client == null
                    ? const _NeedsServer()
                    : IndexedStack(
                        index: _tab,
                        children: [
                          GitChangesView(
                            onShowHistory: () => setState(() => _tab = 1),
                          ),
                          const GitHistoryView(),
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

/// The folder chip in the header: the server's folder or one project.
class _FolderMenu extends StatelessWidget {
  const _FolderMenu({
    required this.directory,
    required this.projects,
    required this.onPick,
  });

  final String? directory;
  final List<Project> projects;

  /// Null picks the server's own working directory.
  final ValueChanged<String?> onPick;

  @override
  Widget build(BuildContext context) {
    final label = directory == null
        ? 'Server folder'
        : nameFromDirectory(directory!);
    return PopupMenuButton<String>(
      tooltip: directory ?? 'The folder gocode was started in',
      onSelected: (v) => onPick(v.isEmpty ? null : v),
      itemBuilder: (_) => [
        const PopupMenuItem(value: '', child: Text('Server folder')),
        if (projects.isNotEmpty) const PopupMenuDivider(),
        for (final p in projects)
          PopupMenuItem(
            value: p.directory,
            child: Text(
              p.name.isEmpty ? nameFromDirectory(p.directory) : p.name,
            ),
          ),
        if (directory != null &&
            !projects.any((p) => p.directory == directory)) ...[
          const PopupMenuDivider(),
          PopupMenuItem(value: directory, child: Text(directory!)),
        ],
      ],
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
        decoration: BoxDecoration(
          color: GC.surface3,
          borderRadius: BorderRadius.circular(999),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.folder_outlined, size: 13, color: GC.accentText),
            const SizedBox(width: 5),
            Flexible(
              child: Text(
                label,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(
                  fontFamily: GC.sans,
                  fontSize: 12,
                  color: GC.textBody,
                ),
              ),
            ),
            const Icon(Icons.expand_more_rounded, size: 14, color: GC.textDim),
          ],
        ),
      ),
    );
  }
}

/// ACP mode speaks the Agent Client Protocol only: no HTTP API, so no git.
class _NeedsServer extends StatelessWidget {
  const _NeedsServer();

  @override
  Widget build(BuildContext context) => Center(
    child: Padding(
      padding: const EdgeInsets.all(24),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.account_tree_outlined, size: 28, color: GC.textDim),
          const SizedBox(height: 12),
          Text(
            'Source Control needs Server or Remote mode',
            style: Theme.of(context).textTheme.titleSmall,
          ),
          const SizedBox(height: 6),
          const Text(
            'ACP mode has no HTTP API. Switch the connection mode in Settings.',
            textAlign: TextAlign.center,
          ),
        ],
      ),
    ),
  );
}

/// The route to Source Control for [directory] (null: the default folder).
String sourceControlLocation({String? directory}) => Uri(
  path: '/git',
  queryParameters: directory == null || directory.isEmpty
      ? null
      : {'dir': directory},
).toString();

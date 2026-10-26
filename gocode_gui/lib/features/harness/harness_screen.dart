import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/glass.dart';
import '../sidebar/projects.dart';
import 'harness_start_dialog.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// Harness² runs: running above finished, newest first — the same order and
/// grouping the TUI's `/harness` shows (documentation/HARNESS2_GUI.md §5.2).
class HarnessScreen extends ConsumerStatefulWidget {
  const HarnessScreen({super.key});

  @override
  ConsumerState<HarnessScreen> createState() => _HarnessScreenState();
}

class _HarnessScreenState extends ConsumerState<HarnessScreen> {
  /// null: all projects; otherwise a workspace path to filter on (S2).
  String? _project;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final available = ref.watch(harnessAvailableProvider);
    final state = ref.watch(harnessRunsProvider);
    final projects = ref.watch(projectsProvider);
    final selected = findProject(projects, ref.watch(selectedProjectProvider));

    // Like Source Control: the selected project is the default filter.
    if (_project == null && selected != null && _sawProjects(projects)) {
      _project = selected.directory;
    }

    // ⌘N / Ctrl+N opens the start flow (§5.8); Esc is the dialog's own.
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.keyN, meta: true): () =>
            _newRun(context),
        const SingleActivator(LogicalKeyboardKey.keyN, control: true): () =>
            _newRun(context),
      },
      child: Scaffold(
        extendBodyBehindAppBar: true,
        appBar: PillHeader(
          leading: const HeaderBackButton(),
          title: Text('Harness', style: theme.textTheme.titleSmall),
          actions: [
            if (available) ...[
              IconButton(
                key: HarnessKeys.refresh,
                tooltip: 'Refresh',
                icon: const Icon(Icons.refresh_rounded),
                onPressed: () =>
                    ref.read(harnessRunsProvider.notifier).reconcile(),
              ),
              TextButton(
                key: HarnessKeys.library,
                onPressed: () => context.go('/harness/library'),
                child: const Text('Library'),
              ),
              const SizedBox(width: 6),
              FilledButton(
                key: HarnessKeys.newRun,
                onPressed: () => _newRun(context),
                child: const Text('New run'),
              ),
              const SizedBox(width: 6),
            ],
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
                constraints: const BoxConstraints(maxWidth: 900),
                child: !available
                    ? const _NeedsServer()
                    : Column(
                        crossAxisAlignment: CrossAxisAlignment.stretch,
                        children: [
                          if (projects.isNotEmpty) _filterRow(projects),
                          Expanded(child: _body(context, state, _project)),
                        ],
                      ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  bool _sawProjects(List<Project> projects) => projects.isNotEmpty;

  Widget _filterRow(List<Project> projects) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Wrap(
        spacing: 8,
        children: [
          _FilterChip(
            key: HarnessKeys.projectFilter,
            label: 'All projects',
            selected: _project == null,
            onTap: () => setState(() => _project = null),
          ),
          for (final p in projects)
            _FilterChip(
              label: p.name.isEmpty ? nameFromDirectory(p.directory) : p.name,
              selected: _project == p.directory,
              onTap: () => setState(() => _project = p.directory),
            ),
        ],
      ),
    );
  }

  Widget _body(BuildContext context, HarnessRunsState state, String? project) {
    if (state.unavailable) return const _NeedsServer();
    if (state.loading && state.runs.isEmpty) {
      return ListView(
        children: [
          for (var i = 0; i < 4; i++)
            const Padding(
              padding: EdgeInsets.only(bottom: 10),
              child: _SkeletonCard(),
            ),
        ],
      );
    }
    if (state.error != null && state.runs.isEmpty) {
      return Center(
        child: ErrorPanel(
          message: state.error!,
          onRetry: () => ref.read(harnessRunsProvider.notifier).reconcile(),
        ),
      );
    }
    final filtered = project == null
        ? state.runs
        : state.runs
              .where((r) => r.workspace == project || r.workspace.isEmpty)
              .toList();
    if (filtered.isEmpty) {
      return _EmptyState(onNewRun: () => _newRun(context));
    }
    final running = filtered.where((r) => r.isRunning).toList();
    final finished = filtered.where((r) => !r.isRunning).toList();
    return ListView(
      children: [
        if (running.isNotEmpty) ...[
          const Caption('Running'),
          const SizedBox(height: 8),
          for (final run in running)
            Padding(
              padding: const EdgeInsets.only(bottom: 10),
              child: _RunCard(run: run),
            ),
        ],
        if (finished.isNotEmpty) ...[
          const Caption('Finished'),
          const SizedBox(height: 8),
          for (final run in finished)
            Padding(
              padding: const EdgeInsets.only(bottom: 10),
              child: _RunCard(run: run),
            ),
        ],
      ],
    );
  }

  Future<void> _newRun(BuildContext context) {
    final selected = findProject(
      ref.read(projectsProvider),
      ref.read(selectedProjectProvider),
    );
    return showHarnessStartDialog(context, workspace: selected?.directory);
  }
}

class _FilterChip extends StatelessWidget {
  const _FilterChip({
    super.key,
    required this.label,
    required this.selected,
    required this.onTap,
  });

  final String label;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return InkWell(
      borderRadius: BorderRadius.circular(999),
      onTap: onTap,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
        decoration: BoxDecoration(
          color: selected ? const Color(0x29E8862D) : const Color(0x0AFFFFFF),
          borderRadius: BorderRadius.circular(999),
          border: Border.all(
            color: selected ? GC.borderAccent : GC.borderStrong,
          ),
        ),
        child: Text(
          label,
          style: TextStyle(
            fontFamily: GC.sans,
            fontSize: 12.5,
            fontWeight: FontWeight.w500,
            color: selected ? GC.accentText : GC.textBody,
          ),
        ),
      ),
    );
  }
}

class _SkeletonCard extends StatelessWidget {
  const _SkeletonCard();

  @override
  Widget build(BuildContext context) {
    return GlassSurface(
      padding: const EdgeInsets.all(16),
      shadow: false,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Container(
            width: 200,
            height: 14,
            decoration: BoxDecoration(
              color: const Color(0x14FFFFFF),
              borderRadius: BorderRadius.circular(4),
            ),
          ),
          const SizedBox(height: 10),
          Container(
            width: 340,
            height: 12,
            decoration: BoxDecoration(
              color: const Color(0x0DFFFFFF),
              borderRadius: BorderRadius.circular(4),
            ),
          ),
        ],
      ),
    );
  }
}

/// One run as a card: task id, prompt excerpt, mode/phase or adopted label,
/// status chip, cost, and a progress bar of settled rows while running.
class _RunCard extends ConsumerWidget {
  const _RunCard({required this.run});

  final HarnessRun run;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final subtitle = run.isRunning
        ? [
            '${run.mode} k=${run.k}',
            if (run.phase.isNotEmpty) run.phase,
            '${run.rowsDone}/${run.rows.length}',
          ].join(' · ')
        : [
            '${run.mode} k=${run.k}',
            if (run.adopted.isNotEmpty) 'adopted ${run.adopted}',
          ].join(' · ');
    return GlassSurface(
      onTap: () => context.go('/harness/run/${run.id}'),
      padding: const EdgeInsets.fromLTRB(16, 14, 8, 14),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        run.task,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.titleSmall,
                      ),
                    ),
                    const SizedBox(width: 8),
                    HarnessStatusChip(status: run.status),
                  ],
                ),
                if (run.prompt.isNotEmpty) ...[
                  const SizedBox(height: 4),
                  Text(
                    run.prompt,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodySmall,
                  ),
                ],
                const SizedBox(height: 6),
                Text(subtitle, style: theme.textTheme.labelSmall),
                if (run.isRunning && run.rows.isNotEmpty) ...[
                  const SizedBox(height: 10),
                  ClipRRect(
                    borderRadius: BorderRadius.circular(999),
                    child: SizedBox(
                      height: 3,
                      child: LinearProgressIndicator(
                        value: run.rowsDone / run.rows.length,
                        minHeight: 3,
                      ),
                    ),
                  ),
                ],
              ],
            ),
          ),
          const SizedBox(width: 12),
          Padding(
            padding: const EdgeInsets.only(top: 2),
            child: HarnessCost(run.costUSD),
          ),
          _RunMenu(run: run),
        ],
      ),
    );
  }
}

class _RunMenu extends ConsumerWidget {
  const _RunMenu({required this.run});

  final HarnessRun run;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return PopupMenuButton<String>(
      tooltip: 'Run actions',
      onSelected: (action) => switch (action) {
        'cancel' => _cancel(context, ref),
        'delete' => _delete(context, ref),
        _ => null,
      },
      itemBuilder: (_) => [
        if (run.isRunning)
          compactMenuItem('cancel', 'Cancel')
        else
          compactMenuItem('delete', 'Delete'),
      ],
    );
  }

  Future<void> _cancel(BuildContext context, WidgetRef ref) async {
    try {
      await ref.read(harnessRunsProvider.notifier).cancel(run.id);
    } catch (e) {
      if (context.mounted) _snack(context, '$e');
    }
  }

  Future<void> _delete(BuildContext context, WidgetRef ref) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('Delete ${run.task}?'),
        content: const Text(
          'The run’s directory and every rollout and editor session under it '
          'go. The experience library is kept — a stored lesson outlives the '
          'run that taught it.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            key: HarnessKeys.delete,
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
    if (confirmed != true) return;
    try {
      await ref.read(harnessRunsProvider.notifier).delete(run.id);
    } catch (e) {
      if (context.mounted) _snack(context, '$e');
    }
  }
}

void _snack(BuildContext context, String text) {
  ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(text)));
}

class _EmptyState extends StatelessWidget {
  const _EmptyState({required this.onNewRun});

  final VoidCallback onNewRun;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(
              Icons.auto_fix_high_outlined,
              size: 28,
              color: GC.textDim,
            ),
            const SizedBox(height: 12),
            Text('No harness runs yet', style: theme.textTheme.titleSmall),
            const SizedBox(height: 6),
            const Text(
              'Refine the harness for a task: open a session and use ✦, or '
              'start one here.',
              textAlign: TextAlign.center,
            ),
            const SizedBox(height: 16),
            FilledButton(
              key: HarnessKeys.newRun,
              onPressed: onNewRun,
              child: const Text('New run'),
            ),
          ],
        ),
      ),
    );
  }
}

/// ACP mode speaks the Agent Client Protocol only: no HTTP API, so no
/// Harness². Mirrors Source Control's disconnected state.
class _NeedsServer extends StatelessWidget {
  const _NeedsServer();

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(
              Icons.auto_fix_high_outlined,
              size: 28,
              color: GC.textDim,
            ),
            const SizedBox(height: 12),
            Text(
              'Harness needs Server or Remote mode',
              style: theme.textTheme.titleSmall,
            ),
            const SizedBox(height: 6),
            const Text(
              'ACP mode has no HTTP API. Switch the connection mode in '
              'Settings.',
              textAlign: TextAlign.center,
            ),
            const SizedBox(height: 16),
            OutlinedButton(
              onPressed: () => context.go('/settings'),
              child: const Text('Open Settings'),
            ),
          ],
        ),
      ),
    );
  }
}

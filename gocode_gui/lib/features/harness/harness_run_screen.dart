import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:gpt_markdown/gpt_markdown.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/glass.dart';
import 'harness_install_dialog.dart';
import 'harness_stage_sheet.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// One run: its plan as a vertical phase timeline (base → propose and probe
/// → compose → select → reflect) that fills in live, the outcome when it
/// finishes, and the run's actions (documentation/HARNESS2_GUI.md §5.4).
class HarnessRunScreen extends ConsumerWidget {
  const HarnessRunScreen({super.key, required this.id});

  final String id;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final run = ref.watch(harnessRunProvider(id));

    return Scaffold(
      extendBodyBehindAppBar: true,
      appBar: PillHeader(
        leading: const HeaderBackButton(),
        title: run == null
            ? Text('Harness run', style: theme.textTheme.titleSmall)
            : Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(run.task, style: theme.textTheme.titleSmall),
                  Text(
                    [
                      '${run.mode} k=${run.k}',
                      if (run.backend.isNotEmpty) run.backend,
                      harnessMoney(run.costUSD),
                    ].join(' · '),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodySmall,
                  ),
                ],
              ),
        actions: [
          if (run != null) ..._actions(context, ref, run),
          const SizedBox(width: 6),
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
              constraints: const BoxConstraints(maxWidth: 760),
              child: run == null
                  ? Center(
                      child: ErrorPanel(
                        message: 'No run $id',
                        onRetry: () =>
                            ref.read(harnessRunsProvider.notifier).reconcile(),
                      ),
                    )
                  : _RunBody(run: run),
            ),
          ),
        ),
      ),
    );
  }

  List<Widget> _actions(BuildContext context, WidgetRef ref, HarnessRun run) {
    return [
      if (run.isRunning)
        OutlinedButton(
          key: HarnessKeys.cancel,
          onPressed: () => _cancel(context, ref),
          child: const Text('Cancel'),
        )
      else ...[
        if (run.status == 'ok') ...[
          if (run.backend == 'split' || run.backend == 'cloud')
            OutlinedButton(
              key: HarnessKeys.publish,
              onPressed: () => showHarnessInstallDialog(
                context,
                runID: run.id,
                to: 'library',
              ),
              child: const Text('Publish skills…'),
            ),
          FilledButton(
            key: HarnessKeys.install,
            onPressed: () => showHarnessInstallDialog(
              context,
              runID: run.id,
              to: 'project',
              // S1: install into the project the run worked on, not only
              // the server's working directory.
              dir: run.workspace.isEmpty ? null : run.workspace,
            ),
            child: const Text('Install…'),
          ),
        ],
        PopupMenuButton<String>(
          tooltip: 'Run actions',
          onSelected: (action) => _delete(context, ref),
          itemBuilder: (_) => [compactMenuItem('delete', 'Delete')],
        ),
      ],
    ];
  }

  Future<void> _cancel(BuildContext context, WidgetRef ref) async {
    try {
      await ref.read(harnessRunsProvider.notifier).cancel(id);
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }

  Future<void> _delete(BuildContext context, WidgetRef ref) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Delete this run?'),
        content: const Text(
          'The run’s directory and every rollout and editor session under it '
          'go. The experience library is kept.',
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
      await ref.read(harnessRunsProvider.notifier).delete(id);
      if (context.mounted) context.go('/harness');
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }
}

class _RunBody extends StatelessWidget {
  const _RunBody({required this.run});

  final HarnessRun run;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return ListView(
      children: [
        Row(
          children: [
            HarnessStatusChip(status: run.status),
            const SizedBox(width: 10),
            if (run.backend.isNotEmpty)
              Tooltip(
                message: run.reason.isEmpty
                    ? 'Experience library backend'
                    : run.reason,
                child: StatusPill(label: run.backend, color: GC.textDim),
              ),
            const Spacer(),
            HarnessCost(run.costUSD),
          ],
        ),
        if (run.budget.isNotEmpty) ...[
          const SizedBox(height: 8),
          Text(run.budget, style: GC.code.copyWith(color: GC.textDim)),
        ],
        if (run.isFinished) ...[
          const SizedBox(height: 16),
          _OutcomeCard(run: run),
        ],
        for (final entry in run.rowsByPhase.entries) ...[
          HarnessPhaseHeader(
            label: harnessPhaseLabel(entry.key),
            active: run.isRunning && entry.key == run.phase,
          ),
          for (final row in entry.value) _RowTile(run: run, row: row),
        ],
        if (run.error.isNotEmpty) ...[
          const SizedBox(height: 12),
          ErrorPanel(message: run.error),
        ],
        const SizedBox(height: 24),
        Text(
          run.workspace.isEmpty
              ? 'Workspace: the server’s working directory'
              : 'Workspace: ${run.workspace}',
          style: theme.textTheme.bodySmall,
        ),
      ],
    );
  }
}

/// The finished run's outcome: adopted label, the selector's reasoning, the
/// lesson — the TUI's run view outcome.
class _OutcomeCard extends StatelessWidget {
  const _OutcomeCard({required this.run});

  final HarnessRun run;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return GlassSurface(
      blur: false,
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const Icon(Icons.verified_outlined, size: 16, color: GC.ok),
              const SizedBox(width: 8),
              Text(
                run.adopted.isEmpty
                    ? 'Nothing adopted'
                    : 'Adopted ${run.adopted}',
                style: theme.textTheme.titleSmall,
              ),
            ],
          ),
          if (run.why.isNotEmpty) ...[
            const SizedBox(height: 10),
            GptMarkdown(
              run.why,
              style: GC.reading.copyWith(color: GC.textBody),
            ),
          ],
          const SizedBox(height: 12),
          Caption('Lesson'),
          const SizedBox(height: 6),
          Text(
            run.lesson.isEmpty ? 'No transferable lesson' : run.lesson,
            style: theme.textTheme.bodyMedium,
          ),
        ],
      ),
    );
  }
}

class _RowTile extends StatelessWidget {
  const _RowTile({required this.run, required this.row});

  final HarnessRun run;
  final HarnessRow row;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final adopted = row.label == run.adopted;
    return Padding(
      padding: const EdgeInsets.only(left: 10, bottom: 6),
      child: InkWell(
        borderRadius: BorderRadius.circular(GC.rInput),
        onTap: () => _openStage(context),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 8),
          child: Row(
            children: [
              HarnessRowGlyph(state: row.state),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        Flexible(
                          child: Text(
                            row.label,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: theme.textTheme.titleSmall?.copyWith(
                              color: adopted ? GC.accentText : GC.textHi,
                            ),
                          ),
                        ),
                        if (adopted) ...[
                          const SizedBox(width: 8),
                          _AdoptedBadge(),
                        ],
                        if (row.retries > 0) ...[
                          const SizedBox(width: 8),
                          Text(
                            '↻${row.retries}',
                            style: GC.code.copyWith(
                              fontSize: 11,
                              color: GC.warn,
                            ),
                          ),
                        ],
                      ],
                    ),
                    if (row.detail.isNotEmpty)
                      Text(
                        row.detail,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodySmall,
                      ),
                  ],
                ),
              ),
              if (row.costUSD > 0) ...[
                const SizedBox(width: 12),
                HarnessCost(row.costUSD),
              ],
            ],
          ),
        ),
      ),
    );
  }

  void _openStage(BuildContext context) {
    showHarnessStageSheet(context, runID: run.id, label: row.label);
  }
}

class _AdoptedBadge extends StatelessWidget {
  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
      decoration: BoxDecoration(
        color: GC.accent.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: GC.borderAccent),
      ),
      child: Text(
        'ADOPTED',
        style: TextStyle(
          fontFamily: GC.sans,
          fontSize: 9,
          fontWeight: FontWeight.w600,
          letterSpacing: 0.8,
          color: GC.accentText,
        ),
      ),
    );
  }
}

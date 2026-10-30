import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:gpt_markdown/gpt_markdown.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/glass.dart';
import 'harness_activity.dart';
import 'harness_install_dialog.dart';
import 'harness_resume.dart';
import 'harness_stage_sheet.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// One run: its plan as a vertical phase timeline (base → propose and probe
/// → compose → select → reflect) that fills in live, the outcome when it
/// finishes, and the run's actions (documentation/11-desktop.md, Harness²).
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
                  Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Flexible(
                        child: Text(
                          [
                            '${run.mode} k=${run.k}',
                            if (run.backend.isNotEmpty) run.backend,
                            harnessMoney(run.costUSD),
                          ].join(' · '),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: theme.textTheme.bodySmall,
                        ),
                      ),
                      if (run.isRunning && run.startedAt > 0) ...[
                        Text(' · ', style: theme.textTheme.bodySmall),
                        HarnessElapsed(
                          since: DateTime.fromMillisecondsSinceEpoch(
                            run.startedAt,
                          ),
                          style: theme.textTheme.bodySmall,
                        ),
                      ],
                    ],
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
        if (harnessResumable(run))
          FilledButton(
            key: HarnessKeys.resume,
            onPressed: () => resumeHarnessRun(context, ref, run),
            child: const Text('Resume…'),
          ),
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
        const SizedBox(height: 18),
        _EventLog(run: run),
        const SizedBox(height: 18),
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

class _RowTile extends ConsumerWidget {
  const _RowTile({required this.run, required this.row});

  final HarnessRun run;
  final HarnessRow row;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final adopted = row.label == run.adopted;
    // The session working right now: the editor while editing, the
    // rollout while rolling.
    final working = switch (row.state) {
      'editing' => (row.editSessionID, 'editor'),
      'rolling' => (row.sessionID, null),
      _ => ('', null),
    };
    final activity = working.$1.isEmpty
        ? null
        : ref.watch(harnessActivityProvider.select((m) => m[working.$1]));
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
                      Tooltip(
                        message: row.detail,
                        waitDuration: const Duration(milliseconds: 600),
                        child: Text(
                          row.detail,
                          maxLines: row.state == 'failed' ? 2 : 1,
                          overflow: TextOverflow.ellipsis,
                          style: row.state == 'failed'
                              ? theme.textTheme.bodySmall?.copyWith(
                                  color: GC.downText,
                                )
                              : theme.textTheme.bodySmall,
                        ),
                      ),
                    if (activity != null)
                      HarnessActivityLine(activity: activity, role: working.$2),
                  ],
                ),
              ),
              if (row.costUSD > 0) ...[
                const SizedBox(width: 12),
                HarnessCost(row.costUSD),
              ],
              if (row.editSessionID.isNotEmpty)
                HarnessWatchButton(
                  sessionID: row.editSessionID,
                  tooltip: row.state == 'editing'
                      ? 'Watch the editor live'
                      : 'Open the editor session',
                  icon: Icons.edit_note_rounded,
                ),
              if (row.sessionID.isNotEmpty)
                HarnessWatchButton(
                  sessionID: row.sessionID,
                  tooltip: row.state == 'rolling'
                      ? 'Watch the rollout live'
                      : 'Open the rollout session',
                  icon: Icons.play_circle_outline_rounded,
                ),
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

/// The run's event log (events.jsonl), collapsed by default and followed
/// live while open — the TUI's ctrl+g log panel.
class _EventLog extends ConsumerStatefulWidget {
  const _EventLog({required this.run});

  final HarnessRun run;

  @override
  ConsumerState<_EventLog> createState() => _EventLogState();
}

class _EventLogState extends ConsumerState<_EventLog> {
  bool _open = false;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return GlassSurface(
      blur: false,
      shadow: false,
      padding: const EdgeInsets.fromLTRB(14, 6, 6, 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          InkWell(
            key: HarnessKeys.eventLog,
            borderRadius: BorderRadius.circular(GC.rInput),
            onTap: () => setState(() => _open = !_open),
            child: Padding(
              padding: const EdgeInsets.symmetric(vertical: 6),
              child: Row(
                children: [
                  Icon(
                    _open
                        ? Icons.expand_more_rounded
                        : Icons.chevron_right_rounded,
                    size: 18,
                    color: GC.textDim,
                  ),
                  const SizedBox(width: 6),
                  Text('Event log', style: theme.textTheme.titleSmall),
                  const SizedBox(width: 8),
                  if (_open && widget.run.isRunning)
                    const StatusPill(label: 'following', color: GC.accent),
                ],
              ),
            ),
          ),
          if (_open) _lines(context),
        ],
      ),
    );
  }

  Widget _lines(BuildContext context) {
    final log = ref.watch(harnessLogProvider(widget.run.id));
    return log.when(
      loading: () => const Padding(
        padding: EdgeInsets.all(12),
        child: Center(
          child: SizedBox.square(
            dimension: 16,
            child: CircularProgressIndicator(strokeWidth: 2),
          ),
        ),
      ),
      error: (e, _) => Padding(
        padding: const EdgeInsets.all(8),
        child: ErrorPanel(message: '$e'),
      ),
      data: (events) {
        if (events.isEmpty) {
          return const Padding(
            padding: EdgeInsets.fromLTRB(24, 4, 8, 10),
            child: Text('Nothing logged yet'),
          );
        }
        final start = events.first.time;
        // Newest last, like a terminal tail; capped so a long run stays
        // cheap to lay out.
        final shown = events.length > 400
            ? events.sublist(events.length - 400)
            : events;
        return ConstrainedBox(
          constraints: const BoxConstraints(maxHeight: 360),
          child: ListView.builder(
            shrinkWrap: true,
            reverse: true,
            itemCount: shown.length,
            itemBuilder: (context, i) =>
                _LogLine(event: shown[shown.length - 1 - i], start: start),
          ),
        );
      },
    );
  }
}

class _LogLine extends StatelessWidget {
  const _LogLine({required this.event, required this.start});

  final HarnessEvent event;
  final int start;

  @override
  Widget build(BuildContext context) {
    final at = harnessDuration(Duration(milliseconds: event.time - start));
    final failed = event.isFailure;
    final what = switch (event.type) {
      'phase' => 'phase ${harnessPhaseLabel(event.phase).toLowerCase()}',
      'session' => '${event.role} session ${event.sessionID}',
      'stage.retry' => 'retry #${event.attempt}',
      _ => event.type,
    };
    final text = [
      what,
      if (event.label.isNotEmpty) event.label,
      if (event.type.endsWith('.ended') && failed) 'failed',
      if (event.detail.isNotEmpty) event.detail,
      if (event.costUSD > 0) harnessMoney(event.costUSD),
    ].join(' · ');
    final style = GC.code.copyWith(fontSize: 11.5);
    return Padding(
      padding: const EdgeInsets.fromLTRB(24, 2, 8, 2),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 62,
            child: Text(at, style: style.copyWith(color: GC.textFaint)),
          ),
          Expanded(
            child: SelectableText(
              text,
              style: style.copyWith(
                color: failed
                    ? GC.downText
                    : event.type == 'phase'
                    ? GC.accentText
                    : GC.textBody,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

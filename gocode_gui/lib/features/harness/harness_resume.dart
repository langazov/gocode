import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/harness_models.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// Whether a run can be started again: anything stopped short of ok —
/// cancelled, failed, partial, or interrupted by a crash — the TUI's
/// harnessResumable.
bool harnessResumable(HarnessRun run) =>
    run.status != 'running' && run.status != 'ok' && run.status != 'planned';

/// Confirms, then resumes a stopped run. Spending money always shows its
/// price first: the confirm names the budget and that finished work is
/// reused.
Future<void> resumeHarnessRun(
  BuildContext context,
  WidgetRef ref,
  HarnessRun run,
) async {
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Resume this run?'),
      content: Text(
        'Task ${run.task}, ${run.mode} k=${run.k}, ended ${run.status}.\n\n'
        '${run.budget}.\n\n'
        'Finished stages and rollouts are reused; only what was cut short '
        'runs, and costs, again. It runs on the server’s current model.',
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context, false),
          child: const Text('Cancel'),
        ),
        FilledButton(
          key: HarnessKeys.confirmResume,
          onPressed: () => Navigator.pop(context, true),
          child: const Text('Resume'),
        ),
      ],
    ),
  );
  if (confirmed != true) return;
  try {
    await ref.read(harnessRunsProvider.notifier).resume(run.id);
  } catch (e) {
    if (context.mounted) {
      ScaffoldMessenger.of(
        context,
      ).showSnackBar(SnackBar(content: Text('Could not resume the run: $e')));
    }
  }
}

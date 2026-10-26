import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/glass.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// The install / publish flow (documentation/HARNESS2_GUI.md §5.6): always
/// previewed first — dry-run, show what would change, confirm, then write.
/// "Already holds this harness" short-circuits to a SnackBar with no dialog
/// (the TUI's behaviour).
Future<void> showHarnessInstallDialog(
  BuildContext context, {
  required String runID,
  required String to,
  String? dir,
}) async {
  final scope = ProviderScope.containerOf(context);
  final messenger = ScaffoldMessenger.maybeOf(context);
  final HarnessInstall plan;
  try {
    plan = await scope
        .read(harnessRunsProvider.notifier)
        .install(runID, to: to, dryRun: true, dir: dir);
  } catch (e) {
    messenger?.showSnackBar(SnackBar(content: Text(harnessErrorText(e))));
    return;
  }
  // "Already holds this harness" never opens a dialog — the SnackBar says
  // it (the TUI's behaviour).
  if (to != 'library' && plan.files.unchanged) {
    messenger?.showSnackBar(
      SnackBar(content: Text('${plan.target} already holds this harness')),
    );
    return;
  }
  if (!context.mounted) return;
  await showDialog<void>(
    context: context,
    // The preview already fetched the plan; the dialog shows it rather than
    // asking for it a second time.
    builder: (_) => _InstallDialog(runID: runID, to: to, dir: dir, plan: plan),
  );
}

class _InstallDialog extends ConsumerStatefulWidget {
  const _InstallDialog({
    required this.runID,
    required this.to,
    this.dir,
    required this.plan,
  });

  final String runID;
  final String to; // project, library, both
  final String? dir;

  /// The dry-run's result: what this dialog previews.
  final HarnessInstall plan;

  @override
  ConsumerState<_InstallDialog> createState() => _InstallDialogState();
}

class _InstallDialogState extends ConsumerState<_InstallDialog> {
  late final HarnessInstall _plan = widget.plan;
  String? _error;
  bool _busy = false;

  Future<void> _confirm() async {
    final plan = _plan;
    setState(() => _busy = true);
    final messenger = ScaffoldMessenger.of(context);
    try {
      final result = await ref
          .read(harnessRunsProvider.notifier)
          .install(
            widget.runID,
            to: widget.to,
            force: plan.conflict,
            dir: widget.dir,
          );
      if (!mounted) return;
      Navigator.pop(context);
      final message = result.written
          ? 'Installed into ${result.target} — new sessions use it.'
          : 'Skills published to the Library.';
      messenger.showSnackBar(SnackBar(content: Text(message)));
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = harnessErrorText(e);
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final plan = _plan;
    final publish = widget.to == 'library';
    return CallbackShortcuts(
      bindings: {
        // ⌘↵ / Ctrl+↵ confirms (§5.8).
        const SingleActivator(LogicalKeyboardKey.enter, meta: true): () {
          if (!_busy) _confirm();
        },
        const SingleActivator(LogicalKeyboardKey.enter, control: true): () {
          if (!_busy) _confirm();
        },
      },
      child: AlertDialog(
        title: Text(
          publish ? 'Publish skills to the Library?' : 'Install harness?',
        ),
        content: SizedBox(
          width: 480,
          child: _error != null
              ? ErrorPanel(message: _error!)
              : ListView(
                  shrinkWrap: true,
                  children: [
                    if (!publish) ...[
                      Text(
                        'Writes into ${plan.target}',
                        style: theme.textTheme.bodyMedium,
                      ),
                      const SizedBox(height: 10),
                      HarnessFileList(files: plan.files),
                      if (plan.conflict) ...[
                        const SizedBox(height: 10),
                        Text(
                          'Replaces the harness installed there now.',
                          style: theme.textTheme.bodyMedium?.copyWith(
                            color: GC.warn,
                          ),
                        ),
                      ],
                      if (plan.collisions.isNotEmpty) ...[
                        const SizedBox(height: 8),
                        Text(
                          'Your own ${plan.collisions.join(', ')} keep '
                          'precedence over the harness’s copies.',
                          style: theme.textTheme.bodySmall,
                        ),
                      ],
                    ],
                    if (plan.library != null) ...[
                      const SizedBox(height: 10),
                      Caption('Library skills'),
                      const SizedBox(height: 6),
                      Text(
                        '${plan.library!.newSkills.length} new · '
                        '${plan.library!.changed.length + plan.library!.blocked.length} changed · '
                        '${plan.library!.unchanged.length} unchanged',
                        style: theme.textTheme.bodyMedium,
                      ),
                      if (plan.library!.blocked.isNotEmpty) ...[
                        const SizedBox(height: 4),
                        Text(
                          'Changed skills are replaced: '
                          '${plan.library!.blocked.join(', ')}.',
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: GC.warn,
                          ),
                        ),
                      ],
                    ],
                    for (final warning in plan.warnings)
                      Padding(
                        padding: const EdgeInsets.only(top: 8),
                        child: Text(
                          warning,
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: GC.warn,
                          ),
                        ),
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
            key: HarnessKeys.confirmInstall,
            onPressed: _busy ? null : _confirm,
            child: Text(
              plan.conflict
                  ? 'Replace and install'
                  : publish
                  ? 'Publish'
                  : 'Install',
            ),
          ),
        ],
      ),
    );
  }
}

String harnessErrorText(Object e) {
  if (e is HarnessInstallConflict) return e.message;
  return '$e';
}

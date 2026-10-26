import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:gpt_markdown/gpt_markdown.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/diff_view.dart';
import '../../shared/widgets/glass.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// A stage's detail (documentation/HARNESS2_GUI.md §5.5): a right-hand
/// sheet beside the run view on wide windows, a pushed page on narrow ones.
/// Tabs: Harness (file list + diff), Notes (editor notes), Rollout (the
/// deliverable and its workspace diff).
Future<void> showHarnessStageSheet(
  BuildContext context, {
  required String runID,
  required String label,
}) {
  final wide = MediaQuery.sizeOf(context).width >= 1100;
  if (wide) {
    return showGeneralDialog(
      context: context,
      barrierDismissible: true,
      barrierLabel: 'Close stage',
      barrierColor: const Color(0x66000000),
      transitionDuration: const Duration(milliseconds: 180),
      pageBuilder: (context, animation, _) => Align(
        alignment: Alignment.centerRight,
        child: _StageSheet(runID: runID, label: label, width: 480),
      ),
      transitionBuilder: (context, animation, _, child) => SlideTransition(
        position: Tween<Offset>(
          begin: const Offset(1, 0),
          end: Offset.zero,
        ).animate(CurvedAnimation(parent: animation, curve: GC.ease)),
        child: child,
      ),
    );
  }
  return Navigator.of(context).push(
    MaterialPageRoute<void>(
      builder: (_) => _StagePage(runID: runID, label: label),
    ),
  );
}

class _StagePage extends StatelessWidget {
  const _StagePage({required this.runID, required this.label});

  final String runID;
  final String label;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: PillHeader(
        leading: const HeaderBackButton(),
        title: Text(label, style: Theme.of(context).textTheme.titleSmall),
      ),
      body: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 14),
        child: _StageBody(runID: runID, label: label),
      ),
    );
  }
}

class _StageSheet extends StatelessWidget {
  const _StageSheet({required this.runID, required this.label, this.width});

  final String runID;
  final String label;
  final double? width;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(left: 14, top: 8, bottom: 14),
      child: Align(
        alignment: Alignment.topRight,
        child: SizedBox(
          width: width,
          child: GlassSurface(
            radius: GC.rPanel,
            padding: const EdgeInsets.fromLTRB(18, 18, 18, 12),
            child: SizedBox.expand(
              child: _StageBody(runID: runID, label: label),
            ),
          ),
        ),
      ),
    );
  }
}

class _StageBody extends ConsumerStatefulWidget {
  const _StageBody({required this.runID, required this.label});

  final String runID;
  final String label;

  @override
  ConsumerState<_StageBody> createState() => _StageBodyState();
}

class _StageBodyState extends ConsumerState<_StageBody>
    with SingleTickerProviderStateMixin {
  late final _tabs = TabController(length: 3, vsync: this);

  @override
  void dispose() {
    _tabs.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final stageAsync = ref.watch(
      harnessStageProvider((id: widget.runID, label: widget.label)),
    );

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        stageAsync.when(
          loading: () => const Padding(
            padding: EdgeInsets.all(24),
            child: Center(
              child: SizedBox.square(
                dimension: 18,
                child: CircularProgressIndicator(strokeWidth: 2),
              ),
            ),
          ),
          error: (e, _) => Padding(
            padding: const EdgeInsets.all(16),
            child: ErrorPanel(message: '$e'),
          ),
          data: (stage) => Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(stage.label, style: theme.textTheme.titleSmall),
                        Text(
                          [
                            if (stage.sessionID.isNotEmpty) 'rollout session',
                            stage.delivered ? 'delivered' : 'nothing delivered',
                            if (stage.costUSD > 0) harnessMoney(stage.costUSD),
                          ].join(' · '),
                          style: theme.textTheme.bodySmall,
                        ),
                      ],
                    ),
                  ),
                  if (stage.sessionID.isNotEmpty)
                    Tooltip(
                      message: stage.sessionID,
                      child: TextButton.icon(
                        icon: const Icon(Icons.forum_outlined, size: 16),
                        label: const Text('Open session'),
                        onPressed: () =>
                            context.push('/session/${stage.sessionID}'),
                      ),
                    ),
                  IconButton(
                    tooltip: 'Close',
                    icon: const Icon(Icons.close_rounded),
                    onPressed: () => Navigator.maybePop(context),
                  ),
                ],
              ),
              const SizedBox(height: 8),
              TabBar(
                controller: _tabs,
                tabs: const [
                  Tab(text: 'Harness'),
                  Tab(text: 'Notes'),
                  Tab(text: 'Rollout'),
                ],
              ),
              const SizedBox(height: 8),
            ],
          ),
        ),
        Expanded(
          child: stageAsync.maybeWhen(
            data: (stage) => TabBarView(
              controller: _tabs,
              children: [
                _harnessTab(stage),
                _notesTab(stage),
                _rolloutTab(stage),
              ],
            ),
            orElse: () => const SizedBox.shrink(),
          ),
        ),
      ],
    );
  }

  Widget _harnessTab(HarnessStage stage) {
    return ListView(
      padding: const EdgeInsets.only(bottom: 16),
      children: [
        HarnessFileList(files: stage.files),
        const SizedBox(height: 12),
        if (stage.diff.trim().isEmpty)
          const Text('Same as base')
        else
          DiffView(patch: stage.diff),
      ],
    );
  }

  Widget _notesTab(HarnessStage stage) {
    return ListView(
      padding: const EdgeInsets.only(bottom: 16),
      children: [
        if (stage.notes.isEmpty && stage.error.isEmpty)
          const Text('No editor notes')
        else ...[
          if (stage.notes.isNotEmpty)
            GptMarkdown(
              stage.notes,
              style: GC.reading.copyWith(color: GC.textBody),
            ),
          if (stage.error.isNotEmpty) ...[
            const SizedBox(height: 10),
            ErrorPanel(message: stage.error),
          ],
        ],
      ],
    );
  }

  Widget _rolloutTab(HarnessStage stage) {
    return ListView(
      padding: const EdgeInsets.only(bottom: 16),
      children: [
        if (!stage.delivered && stage.deliverable.isEmpty)
          const Padding(
            padding: EdgeInsets.symmetric(vertical: 16),
            child: Text('Nothing delivered'),
          )
        else ...[
          GptMarkdown(
            stage.finalMessage,
            style: GC.reading.copyWith(color: GC.textBody),
          ),
          if (stage.workspaceDiff.isNotEmpty) ...[
            const SizedBox(height: 12),
            DiffView(patch: stage.workspaceDiff),
          ],
        ],
      ],
    );
  }
}

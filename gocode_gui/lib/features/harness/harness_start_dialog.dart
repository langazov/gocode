import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/client.dart';
import '../../core/api/harness_models.dart';
import '../../core/api/models.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/model_picker.dart';
import '../home/providers.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// The start flow (documentation/HARNESS2_GUI.md §5.3): one dialog with
/// three steps — task, budget, plan — that never leaves it. A run is never
/// started without its price on screen; **Start** is the only button that
/// spends.
Future<void> showHarnessStartDialog(
  BuildContext context, {
  String? prompt,
  String? workspace,
  ModelRef? model,
}) => showDialog<void>(
  context: context,
  barrierColor: const Color(0x99000000),
  builder: (_) => _StartDialog(
    initialPrompt: prompt ?? '',
    workspace: workspace,
    initialModel: model,
  ),
);

/// Turns free text into an id usable as a task id or domain, mirroring the
/// server's harness.Slug.
String harnessSlug(String text) {
  final b = StringBuffer();
  var dash = false;
  for (final r in text.toLowerCase().runes) {
    final ok = (r >= 0x61 && r <= 0x7A) || (r >= 0x30 && r <= 0x39);
    if (ok) {
      b.writeCharCode(r);
      dash = false;
    } else if (!dash && b.length > 0) {
      b.write('-');
      dash = true;
    }
    if (b.length >= 48) break;
  }
  var out = b.toString();
  while (out.startsWith('-')) {
    out = out.substring(1);
  }
  while (out.endsWith('-')) {
    out = out.substring(0, out.length - 1);
  }
  return out.isEmpty ? 'task' : out;
}

class _StartDialog extends ConsumerStatefulWidget {
  const _StartDialog({
    required this.initialPrompt,
    this.workspace,
    this.initialModel,
  });

  final String initialPrompt;
  final String? workspace;
  final ModelRef? initialModel;

  @override
  ConsumerState<_StartDialog> createState() => _StartDialogState();
}

class _StartDialogState extends ConsumerState<_StartDialog> {
  late final _task = TextEditingController(text: widget.initialPrompt);
  late String _id = harnessSlug(widget.initialPrompt);
  String? _domain;
  late String? _modelKey = widget.initialModel?.key;
  String? _improverModelKey;
  String _library = 'auto';
  bool _crossDomain = false;
  HarnessBudget? _budget;
  HarnessRun? _plan;
  bool _planning = false;
  String? _error;
  bool _alreadyRunning = false;
  int _step = 0;

  @override
  void initState() {
    super.initState();
    _alreadyRunning = false;
    _task.addListener(_reseed);
  }

  @override
  void dispose() {
    _task.dispose();
    super.dispose();
  }

  void _reseed() => setState(() {
    final slug = harnessSlug(_task.text);
    if (_id == harnessSlug(_lastText) || _id.isEmpty) _id = slug;
    _lastText = _task.text;
  });

  String _lastText = '';

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final budgets =
        ref.watch(harnessBudgetsProvider).value ?? const <HarnessBudget>[];
    if (_step == 1 && _budget == null && budgets.isNotEmpty) {
      _budget = budgets.first;
    }
    final models = ref.watch(modelsProvider).value ?? const <ModelEntry>[];
    final workspace = widget.workspace;

    return Dialog(
      backgroundColor: Colors.transparent,
      elevation: 0,
      insetPadding: const EdgeInsets.all(24),
      child: CallbackShortcuts(
        bindings: {
          // ⌘↵ / Ctrl+↵ confirms (the composer's convention, §5.8).
          const SingleActivator(LogicalKeyboardKey.enter, meta: true): _confirm,
          const SingleActivator(LogicalKeyboardKey.enter, control: true):
              _confirm,
        },
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 560, maxHeight: 660),
          child: GlassSurface(
            radius: GC.rPanel,
            tint: GC.surface1.withValues(alpha: 0.7),
            padding: const EdgeInsets.fromLTRB(20, 22, 20, 14),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(switch (_step) {
                        0 => 'New harness run',
                        1 => 'Budget',
                        _ => 'Start harness run?',
                      }, style: theme.textTheme.titleLarge),
                    ),
                    if (_step > 0)
                      IconButton(
                        tooltip: 'Back',
                        icon: const Icon(Icons.arrow_back_rounded),
                        onPressed: _back,
                      ),
                  ],
                ),
                const SizedBox(height: 14),
                Expanded(
                  child: switch (_step) {
                    0 => _taskStep(context, workspace),
                    1 => _budgetStep(context, budgets, models),
                    _ => _planStep(context),
                  },
                ),
                if (_error != null) ...[
                  const SizedBox(height: 8),
                  Text(
                    _error!,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: GC.downText,
                    ),
                  ),
                ],
                const SizedBox(height: 12),
                Row(
                  children: [
                    const Spacer(),
                    TextButton(
                      onPressed: () => Navigator.pop(context),
                      child: const Text('Cancel'),
                    ),
                    const SizedBox(width: 8),
                    if (_step == 0)
                      FilledButton(
                        onPressed: _canAdvance ? _advance : null,
                        child: const Text('Next'),
                      )
                    else if (_step == 1)
                      FilledButton(
                        onPressed: _budget == null ? null : _planRun,
                        child: _planning
                            ? const SizedBox.square(
                                dimension: 14,
                                child: CircularProgressIndicator(
                                  strokeWidth: 2,
                                ),
                              )
                            : const Text('Plan'),
                      )
                    else if (_alreadyRunning)
                      FilledButton(
                        key: HarnessKeys.openRun,
                        onPressed: _openRunning,
                        child: const Text('Open run'),
                      )
                    else
                      FilledButton(
                        key: HarnessKeys.start,
                        onPressed: _startRun,
                        child: const Text('Start'),
                      ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  /// The dialog's current primary action: advance, plan, or start — ⌘↵.
  void _confirm() {
    if (_step == 0) {
      if (_canAdvance) _advance();
    } else if (_step == 1) {
      if (_budget != null) _planRun();
    } else if (!_alreadyRunning) {
      _startRun();
    }
  }

  bool get _canAdvance => _task.text.trim().isNotEmpty;

  void _advance() => setState(() => _step = 1);

  void _back() => setState(() {
    if (_step == 2) {
      _plan = null;
      _alreadyRunning = false;
      _error = null;
      _step = 1;
    } else if (_step == 1) {
      _budget = null;
      _step = 0;
    }
  });

  Widget _taskStep(BuildContext context, String? workspace) {
    final theme = Theme.of(context);
    final domainDefault = harnessSlug(widget.workspace?.split('/').last ?? '');
    return ListView(
      shrinkWrap: true,
      children: [
        const Caption('Task'),
        const SizedBox(height: 8),
        TextField(
          key: HarnessKeys.taskField,
          controller: _task,
          autofocus: true,
          minLines: 3,
          maxLines: 6,
          decoration: const InputDecoration(
            hintText: 'Describe the task the harness should get better at',
          ),
        ),
        const SizedBox(height: 16),
        Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Caption('Task id'),
                  const SizedBox(height: 6),
                  TextField(
                    controller: TextEditingController(text: _id)
                      ..addListener(() {}),
                    style: GC.code.copyWith(fontSize: 13),
                    decoration: const InputDecoration(
                      hintText: 'Defaults to a slug of the task',
                    ),
                    onChanged: (v) => _id = v,
                  ),
                ],
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Caption('Domain'),
                  const SizedBox(height: 6),
                  TextField(
                    controller: TextEditingController(
                      text: _domain ?? domainDefault,
                    ),
                    style: GC.code.copyWith(fontSize: 13),
                    decoration: const InputDecoration(
                      hintText: 'Groups related tasks',
                    ),
                    onChanged: (v) => _domain = v,
                  ),
                ],
              ),
            ),
          ],
        ),
        const SizedBox(height: 16),
        const Caption('Workspace'),
        const SizedBox(height: 6),
        Text(
          workspace == null || workspace.isEmpty
              ? 'The server’s working directory'
              : workspace,
          style: GC.code.copyWith(fontSize: 12.5, color: GC.textDim),
        ),
        const SizedBox(height: 6),
        Text(
          'Rollouts run on copies of the workspace; your files are not '
          'touched.',
          style: theme.textTheme.bodySmall,
        ),
      ],
    );
  }

  Widget _budgetStep(
    BuildContext context,
    List<HarnessBudget> budgets,
    List<ModelEntry> models,
  ) {
    final theme = Theme.of(context);
    final model = _modelFor(models);
    return ListView(
      shrinkWrap: true,
      children: [
        ...budgets.map((b) {
          final selected = _budget?.key == b.key;
          return Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: InkWell(
              key: _budget == budgets.first && b == budgets.first
                  ? HarnessKeys.budgetList
                  : null,
              borderRadius: BorderRadius.circular(GC.rInput),
              onTap: () => setState(() => _budget = b),
              child: Ink(
                decoration: BoxDecoration(
                  color: selected
                      ? const Color(0x1FE8862D)
                      : const Color(0x0AFFFFFF),
                  borderRadius: BorderRadius.circular(GC.rInput),
                  border: Border.all(
                    color: selected ? GC.borderAccent : GC.border,
                  ),
                ),
                padding: const EdgeInsets.symmetric(
                  horizontal: 12,
                  vertical: 10,
                ),
                child: Row(
                  children: [
                    SizedBox(
                      width: 16,
                      child: selected
                          ? const Icon(
                              Icons.check_rounded,
                              size: 16,
                              color: GC.accent,
                            )
                          : null,
                    ),
                    const SizedBox(width: 8),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            '${b.mode} k=${b.k}',
                            style: theme.textTheme.titleSmall,
                          ),
                          Text(b.description, style: theme.textTheme.bodySmall),
                        ],
                      ),
                    ),
                    Text(
                      '${b.rollouts} rollouts · ${b.edits} edits',
                      style: GC.code.copyWith(fontSize: 11.5),
                    ),
                  ],
                ),
              ),
            ),
          );
        }),
        const SizedBox(height: 8),
        InkWell(
          borderRadius: BorderRadius.circular(8),
          onTap: () => _pickModel(models),
          child: InputDecorator(
            decoration: const InputDecoration(
              prefixIcon: Icon(Icons.memory_rounded, size: 18),
            ),
            child: Text(
              model ?? 'Server default model',
              style: theme.textTheme.labelMedium,
            ),
          ),
        ),
        const SizedBox(height: 12),
        // The plan's budget step also carries these: the improver's own
        // model, the experience-library backend and cross-domain lessons.
        ExpansionTile(
          tilePadding: EdgeInsets.zero,
          childrenPadding: EdgeInsets.zero,
          title: Text('Advanced', style: theme.textTheme.bodyMedium),
          onExpansionChanged: null,
          children: [
            InkWell(
              borderRadius: BorderRadius.circular(8),
              onTap: () => _pickImproverModel(models),
              child: InputDecorator(
                decoration: const InputDecoration(
                  labelText: 'Editor model',
                  prefixIcon: Icon(Icons.edit_note_rounded, size: 18),
                ),
                child: Text(
                  improverModel ?? 'Same as the executor',
                  style: theme.textTheme.labelMedium,
                ),
              ),
            ),
            const SizedBox(height: 10),
            Row(
              children: [
                Text('Library', style: theme.textTheme.labelMedium),
                const SizedBox(width: 12),
                Expanded(
                  child: SegmentedButton<String>(
                    segments: const [
                      ButtonSegment(value: 'auto', label: Text('Auto')),
                      ButtonSegment(value: 'local', label: Text('Local')),
                      ButtonSegment(value: 'cloud', label: Text('Cloud')),
                    ],
                    selected: {_library},
                    onSelectionChanged: (s) =>
                        setState(() => _library = s.first),
                  ),
                ),
              ],
            ),
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              dense: true,
              title: const Text('Cross-domain lessons'),
              subtitle: const Text(
                'Also read other domains’ stored lessons (hypotheses, never '
                'facts)',
              ),
              value: _crossDomain,
              onChanged: (v) => setState(() => _crossDomain = v),
            ),
          ],
        ),
      ],
    );
  }

  String? _modelFor(List<ModelEntry> models) {
    if (_modelKey == null) return null;
    for (final m in models) {
      if (m.key == _modelKey) return m.name;
    }
    return _modelKey;
  }

  Future<void> _pickModel(List<ModelEntry> models) async {
    final choice = await showModelPicker(
      context,
      models: models,
      selectedKey: _modelKey,
      allowDefault: true,
    );
    if (choice == null) return;
    setState(() => _modelKey = choice.model?.key);
  }

  Future<void> _pickImproverModel(List<ModelEntry> models) async {
    final choice = await showModelPicker(
      context,
      models: models,
      selectedKey: _improverModelKey,
      allowDefault: true,
    );
    if (choice == null) return;
    setState(() => _improverModelKey = choice.model?.key);
  }

  String? get improverModel {
    if (_improverModelKey == null) return null;
    final models = ref.read(modelsProvider).value ?? const <ModelEntry>[];
    for (final m in models) {
      if (m.key == _improverModelKey) return m.name;
    }
    return _improverModelKey;
  }

  HarnessStartRequest get _request {
    final budget = _budget!;
    return HarnessStartRequest(
      prompt: _task.text.trim(),
      id: _id.trim().isEmpty ? null : _id.trim(),
      domain: (_domain?.trim()).isNullOrEmpty ? null : _domain!.trim(),
      workspace: widget.workspace,
      k: budget.k,
      mode: budget.mode,
      model: _modelKey,
      improverModel: _improverModelKey,
      library: _library == 'auto' ? null : _library,
      crossDomain: _crossDomain,
    );
  }

  Future<void> _planRun() async {
    setState(() {
      _planning = true;
      _error = null;
    });
    try {
      final plan = await ref.read(harnessRunsProvider.notifier).plan(_request);
      if (!mounted) return;
      setState(() {
        _plan = plan;
        _planning = false;
        _step = 2;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _planning = false;
        _error = e is ApiException ? e.message : '$e';
      });
    }
  }

  Future<void> _startRun() async {
    setState(() => _error = null);
    try {
      final run = await ref.read(harnessRunsProvider.notifier).start(_request);
      if (!mounted) return;
      Navigator.pop(context);
      context.go('/harness/run/${run.id}');
    } on ApiException catch (e) {
      if (!mounted) return;
      // A 409 means the run is already going — here or in another process:
      // offer to open it instead of starting a duplicate.
      setState(() {
        _alreadyRunning = e.status == 409;
        _error = e.message;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = '$e';
      });
    }
  }

  void _openRunning() {
    Navigator.pop(context);
    // The 409 names no id; the runs list has the one that is going.
    final runs = ref.read(harnessRunsProvider).runs;
    for (final run in runs) {
      if (run.isRunning && run.task == _id.trim()) {
        context.go('/harness/run/${run.id}');
        return;
      }
    }
    context.go('/harness');
  }

  Widget _planStep(BuildContext context) {
    final theme = Theme.of(context);
    final plan = _plan!;
    return ListView(
      shrinkWrap: true,
      children: [
        Text(
          'Task ${plan.task} in ${plan.domain}, on ${_modelKey ?? 'the server default'}.',
          style: theme.textTheme.bodyMedium,
        ),
        const SizedBox(height: 10),
        Text(plan.budget, style: GC.code.copyWith(color: GC.accentText)),
        const SizedBox(height: 10),
        Text(
          'Experience library: ${plan.backend}'
          '${plan.reason.isEmpty ? '' : ' (${plan.reason})'}.',
          style: theme.textTheme.bodySmall,
        ),
        const SizedBox(height: 12),
        for (final entry in plan.rowsByPhase.entries) ...[
          HarnessPhaseHeader(label: harnessPhaseLabel(entry.key)),
          for (final row in entry.value)
            Padding(
              padding: const EdgeInsets.only(left: 14, bottom: 2),
              child: Text(
                row.label,
                style: GC.code.copyWith(fontSize: 12, color: GC.textDim),
              ),
            ),
        ],
        const SizedBox(height: 12),
        Text(
          'Rollouts run on copies of the workspace; your files are not '
          'touched.',
          style: theme.textTheme.bodySmall?.copyWith(color: GC.textDim),
        ),
      ],
    );
  }
}

extension on String? {
  bool get isNullOrEmpty => this == null || this!.isEmpty;
}

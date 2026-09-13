import 'dart:io';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import '../../core/connection/controller.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/model_picker.dart';
import 'providers.dart';

/// New-session flow: pick a directory, optionally an agent and a model, then
/// create the session. Agent and model default to the server's choice, so a
/// directory is the only thing that's required.
class NewSessionScreen extends ConsumerStatefulWidget {
  const NewSessionScreen({super.key});

  @override
  ConsumerState<NewSessionScreen> createState() => _NewSessionScreenState();
}

class _NewSessionScreenState extends ConsumerState<NewSessionScreen> {
  final _title = TextEditingController();
  late final TextEditingController _directory;

  /// Null until the user picks one; the server's default applies until then.
  String? _agent;
  ModelEntry? _model;
  bool _creating = false;
  String? _error;

  bool get _desktop =>
      Platform.isMacOS || Platform.isLinux || Platform.isWindows;

  @override
  void initState() {
    super.initState();
    _directory = TextEditingController(
      text: ref.read(settingsProvider).workingDirectory,
    );
    // A remote server doesn't report its project directory; borrow the most
    // recent session's so the common case needs no typing.
    ref.listenManual(sessionsProvider, (_, next) {
      final sessions = next.value;
      if (_directory.text.isNotEmpty || sessions == null || sessions.isEmpty) {
        return;
      }
      final latest = sessions.reduce(
        (a, b) => a.timeUpdated >= b.timeUpdated ? a : b,
      );
      _directory.text = latest.directory;
    }, fireImmediately: true);
    _directory.addListener(_rebuild);
  }

  void _rebuild() => setState(() {});

  @override
  void dispose() {
    _title.dispose();
    _directory.dispose();
    super.dispose();
  }

  Future<void> _pickDirectory() async {
    final result = await FilePicker.getDirectoryPath();
    if (result != null) _directory.text = result;
  }

  Future<void> _pickModel(List<ModelEntry> models) async {
    final choice = await showModelPicker(
      context,
      models: models,
      selectedKey: _model?.key,
      allowDefault: true,
    );
    if (choice != null) setState(() => _model = choice.model);
  }

  Future<void> _create() async {
    final client = ref.read(apiClientProvider);
    final dir = _directory.text.trim();
    if (client == null || dir.isEmpty) return;
    setState(() {
      _creating = true;
      _error = null;
    });
    try {
      final session = await client.createSession(
        directory: dir,
        title: _title.text.trim(),
      );
      final model = _model;
      if (model != null) {
        await client.setModel(session.id, model.providerID, model.id);
      }
      final agent = _agent;
      if (agent != null && agent != session.agent) {
        await client.setAgent(session.id, agent);
      }
      ref.invalidate(sessionsProvider);
      // Replace the stack so Back from the session lands on home.
      if (mounted) context.go('/session/${session.id}');
    } catch (e) {
      if (mounted) setState(() => _error = '$e');
    } finally {
      if (mounted) setState(() => _creating = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final settings = ref.watch(settingsProvider);
    final agents = ref.watch(agentsProvider);
    final models = ref.watch(modelsProvider);
    final remote = settings.mode == ConnectionMode.remote;
    final canCreate = !_creating && _directory.text.trim().isNotEmpty;

    return Scaffold(
      extendBodyBehindAppBar: true,
      appBar: const PillHeader(leading: HeaderBackButton()),
      // Built inside the Scaffold: only there do the insets include the
      // floating header the body extends behind.
      body: Builder(
        builder: (context) => ListView(
          padding: EdgeInsets.fromLTRB(
            16,
            MediaQuery.paddingOf(context).top + 28,
            16,
            48,
          ),
          children: [
            Align(
              alignment: Alignment.topCenter,
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 640),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    const Caption('Start'),
                    const SizedBox(height: 6),
                    Text('New session', style: theme.textTheme.headlineMedium),
                    const SizedBox(height: 8),
                    Text(
                      'Choose where the agent works and which model drives it.',
                      style: theme.textTheme.bodyMedium,
                    ),
                    const SizedBox(height: 24),
                    GlassSurface(
                      blur: false,
                      radius: GC.rPanel,
                      padding: const EdgeInsets.all(24),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.stretch,
                        children: [
                          const _Label('Project directory'),
                          Row(
                            children: [
                              Expanded(
                                child: TextField(
                                  controller: _directory,
                                  style: GC.code.copyWith(
                                    fontSize: 13.5,
                                    color: GC.textHi,
                                  ),
                                  decoration: const InputDecoration(
                                    hintText: '/path/to/project',
                                  ),
                                ),
                              ),
                              if (_desktop && !remote) ...[
                                const SizedBox(width: 8),
                                OutlinedButton(
                                  onPressed: _pickDirectory,
                                  child: const Text('Browse'),
                                ),
                              ],
                            ],
                          ),
                          if (remote) ...[
                            const SizedBox(height: 6),
                            Text(
                              'The path as the server sees it.',
                              style: theme.textTheme.bodySmall,
                            ),
                          ],
                          const SizedBox(height: 22),
                          const _Label('Title'),
                          TextField(
                            controller: _title,
                            decoration: const InputDecoration(
                              hintText:
                                  'Optional — defaults to the folder name',
                            ),
                          ),
                          const SizedBox(height: 22),
                          const _Label('Agent'),
                          agents.when(
                            loading: () => const _Loading('Loading agents…'),
                            error: (e, _) => ErrorPanel(
                              message: 'Could not load agents: $e',
                              onRetry: () => ref.invalidate(agentsProvider),
                            ),
                            data: (list) => _AgentChoices(
                              agents: list,
                              selected: _agent,
                              onSelected: (id) => setState(() => _agent = id),
                            ),
                          ),
                          const SizedBox(height: 22),
                          const _Label('Model'),
                          models.when(
                            loading: () => const _Loading('Loading models…'),
                            error: (e, _) => ErrorPanel(
                              message: 'Could not load models: $e',
                              onRetry: () => ref.invalidate(modelsProvider),
                            ),
                            data: (list) => _PickerField(
                              title: _model?.name ?? 'Server default',
                              subtitle:
                                  _model?.key ??
                                  '${list.length} models available',
                              onTap: () => _pickModel(list),
                            ),
                          ),
                          if (_error != null) ...[
                            const SizedBox(height: 20),
                            ErrorPanel(message: _error!),
                          ],
                          const SizedBox(height: 28),
                          FilledButton.icon(
                            onPressed: canCreate ? _create : null,
                            icon: _creating
                                ? const SizedBox.square(
                                    dimension: 16,
                                    child: CircularProgressIndicator(
                                      strokeWidth: 2,
                                      color: GC.accentInk,
                                    ),
                                  )
                                : const Icon(
                                    Icons.arrow_forward_rounded,
                                    size: 18,
                                  ),
                            label: const Text('Start session'),
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// Primary agents as pills; the first pill (no explicit choice) is the
/// server's default agent.
class _AgentChoices extends StatelessWidget {
  const _AgentChoices({
    required this.agents,
    required this.selected,
    required this.onSelected,
  });

  final List<Agent> agents;
  final String? selected;
  final ValueChanged<String?> onSelected;

  @override
  Widget build(BuildContext context) {
    final primary = agents
        .where((a) => !a.hidden && a.mode != 'subagent')
        .toList();
    Agent? current;
    for (final a in primary) {
      if (a.id == selected) current = a;
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            _pill('default', selected == null, () => onSelected(null)),
            for (final a in primary)
              _pill(a.id, a.id == selected, () => onSelected(a.id)),
          ],
        ),
        if (current?.description case final description?) ...[
          const SizedBox(height: 8),
          Text(
            description,
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ],
      ],
    );
  }

  Widget _pill(String label, bool isSelected, VoidCallback onTap) {
    return ChoiceChip(
      label: Text(label),
      selected: isSelected,
      onSelected: (_) => onTap(),
      side: BorderSide(color: isSelected ? GC.borderAccent : GC.borderStrong),
      labelStyle: TextStyle(
        fontFamily: GC.sans,
        fontSize: 13.5,
        fontWeight: isSelected ? FontWeight.w600 : FontWeight.w500,
        color: isSelected ? GC.accentText : GC.textBody,
      ),
    );
  }
}

class _PickerField extends StatelessWidget {
  const _PickerField({
    required this.title,
    required this.subtitle,
    required this.onTap,
  });

  final String title;
  final String subtitle;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Material(
      type: MaterialType.transparency,
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(GC.rInput),
        child: Ink(
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 11),
          decoration: BoxDecoration(
            color: const Color(0x0AFFFFFF),
            borderRadius: BorderRadius.circular(GC.rInput),
            border: Border.all(color: GC.borderStrong),
          ),
          child: Row(
            children: [
              const Icon(Icons.memory_rounded, size: 18, color: GC.accentText),
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(title, style: theme.textTheme.titleSmall),
                    Text(
                      subtitle,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: GC.code.copyWith(
                        fontSize: 11.5,
                        color: GC.textFaint,
                      ),
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

class _Label extends StatelessWidget {
  const _Label(this.text);

  final String text;

  @override
  Widget build(BuildContext context) =>
      Padding(padding: const EdgeInsets.only(bottom: 8), child: Caption(text));
}

class _Loading extends StatelessWidget {
  const _Loading(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        const SizedBox.square(
          dimension: 14,
          child: CircularProgressIndicator(strokeWidth: 2),
        ),
        const SizedBox(width: 10),
        Text(text, style: Theme.of(context).textTheme.bodySmall),
      ],
    );
  }
}

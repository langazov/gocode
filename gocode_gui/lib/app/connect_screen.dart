import 'dart:math' as math;

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../core/connection/controller.dart';
import '../shared/widgets/glass.dart';
import 'theme.dart';

/// First-run gate: pick a project directory (local mode) or point at a
/// server (remote mode), then connect. Errors land inline, not in a loop.
class ConnectScreen extends ConsumerStatefulWidget {
  const ConnectScreen({super.key});

  @override
  ConsumerState<ConnectScreen> createState() => _ConnectScreenState();
}

class _ConnectScreenState extends ConsumerState<ConnectScreen> {
  bool _expanded = false;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final connection = ref.watch(connectionProvider);
    final settings = ref.watch(settingsProvider);
    final connecting = connection.phase == ConnectionPhase.connecting;
    // A configuration error means the inline form should be open.
    final errored = connection.phase == ConnectionPhase.error;

    return Scaffold(
      body: Center(
        child: SingleChildScrollView(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 32),
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 520),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                const Center(child: Eyebrow('AI coding assistant')),
                const SizedBox(height: 28),
                const Center(child: GocodeLogo(size: 56)),
                const SizedBox(height: 14),
                Text(
                  'Pick a project to start a local server, or attach to one '
                  'that is already running.',
                  textAlign: TextAlign.center,
                  style: theme.textTheme.bodyLarge,
                ),
                const SizedBox(height: 32),
                GlassSurface(
                  radius: GC.rPanel,
                  padding: const EdgeInsets.all(24),
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      if (connecting)
                        _Starting(
                          remote: settings.mode == ConnectionMode.remote,
                          log: connection.stderr,
                        )
                      else if (!_expanded && !errored) ...[
                        FilledButton.icon(
                          onPressed: _pickDirectoryAndConnect,
                          icon: const Icon(Icons.folder_open_outlined, size: 18),
                          label: const Text('Choose project folder'),
                        ),
                        const SizedBox(height: 12),
                        OutlinedButton.icon(
                          onPressed: () => setState(() => _expanded = true),
                          icon: const Icon(Icons.lan_outlined, size: 18),
                          label: const Text('Connect to a server'),
                        ),
                      ] else ...[
                        if (connection.error != null) ...[
                          ErrorPanel(
                            message: connection.error!,
                            details: connection.stderr,
                          ),
                          const SizedBox(height: 20),
                        ],
                        _SetupForm(
                          settings: settings,
                          onApplied: () => setState(() => _expanded = false),
                          onCancel: () async {
                            setState(() => _expanded = false);
                            if (errored) {
                              await ref
                                  .read(connectionProvider.notifier)
                                  .disconnect();
                            }
                          },
                        ),
                      ],
                    ],
                  ),
                ),
                const SizedBox(height: 16),
                Center(
                  child: TextButton(
                    onPressed: () => context.go('/settings'),
                    child: const Text('Advanced settings'),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Future<void> _pickDirectoryAndConnect() async {
    final directory = await FilePicker.getDirectoryPath();
    if (directory == null) return;
    final settings = ref.read(settingsProvider).copyWith(
          mode: ConnectionMode.local,
          workingDirectory: directory,
        );
    ref.read(settingsProvider.notifier).update(settings);
    await ref.read(connectionProvider.notifier).apply(settings);
  }
}

class _Starting extends StatelessWidget {
  const _Starting({required this.remote, required this.log});

  final bool remote;
  final List<String> log;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final tail = log.skip(math.max(0, log.length - 6)).join('\n');
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            const SizedBox.square(
              dimension: 18,
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
            const SizedBox(width: 14),
            Text(
              remote ? 'Connecting to server…' : 'Starting gocode server…',
              style: theme.textTheme.titleSmall,
            ),
          ],
        ),
        if (tail.isNotEmpty) ...[
          const SizedBox(height: 16),
          CodeBlock(tail),
        ],
      ],
    );
  }
}

/// The inline configuration form (remote, or retry with edited settings).
class _SetupForm extends ConsumerStatefulWidget {
  const _SetupForm({
    required this.settings,
    required this.onApplied,
    required this.onCancel,
  });

  final ConnectionSettings settings;
  final VoidCallback onApplied;
  final VoidCallback onCancel;

  @override
  ConsumerState<_SetupForm> createState() => _SetupFormState();
}

class _SetupFormState extends ConsumerState<_SetupForm> {
  late ConnectionMode _mode;
  late final TextEditingController _directory;
  late final TextEditingController _url;

  @override
  void initState() {
    super.initState();
    _mode = widget.settings.mode;
    _directory = TextEditingController(text: widget.settings.workingDirectory);
    _url = TextEditingController(text: widget.settings.remoteUrl);
  }

  @override
  void dispose() {
    _directory.dispose();
    _url.dispose();
    super.dispose();
  }

  Future<void> _apply() async {
    final settings = widget.settings.copyWith(
      mode: _mode,
      workingDirectory: _directory.text.trim(),
      remoteUrl: _url.text.trim(),
    );
    ref.read(settingsProvider.notifier).update(settings);
    await ref.read(connectionProvider.notifier).apply(settings);
    if (mounted) widget.onApplied();
  }

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        SegmentedButton<ConnectionMode>(
          segments: const [
            ButtonSegment(
              value: ConnectionMode.local,
              label: Text('Local'),
              icon: Icon(Icons.terminal, size: 18),
            ),
            ButtonSegment(
              value: ConnectionMode.remote,
              label: Text('Remote'),
              icon: Icon(Icons.lan_outlined, size: 18),
            ),
          ],
          selected: {_mode},
          showSelectedIcon: false,
          onSelectionChanged: (s) => setState(() => _mode = s.first),
        ),
        const SizedBox(height: 16),
        if (_mode == ConnectionMode.local)
          Row(
            children: [
              Expanded(
                child: TextField(
                  controller: _directory,
                  style: GC.code.copyWith(fontSize: 13.5, color: GC.textHi),
                  decoration:
                      const InputDecoration(labelText: 'Project directory'),
                ),
              ),
              const SizedBox(width: 8),
              OutlinedButton(
                onPressed: () async {
                  final directory = await FilePicker.getDirectoryPath();
                  if (directory != null) _directory.text = directory;
                },
                child: const Text('Browse'),
              ),
            ],
          )
        else
          TextField(
            controller: _url,
            style: GC.code.copyWith(fontSize: 13.5, color: GC.textHi),
            decoration: const InputDecoration(
              labelText: 'Server URL',
              hintText: 'http://host:port',
            ),
          ),
        const SizedBox(height: 20),
        Row(
          children: [
            TextButton(onPressed: widget.onCancel, child: const Text('Back')),
            const Spacer(),
            FilledButton.icon(
              onPressed: _apply,
              icon: const Icon(Icons.bolt_rounded, size: 18),
              label: const Text('Connect'),
            ),
          ],
        ),
      ],
    );
  }
}

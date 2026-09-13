import 'dart:async';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/connection/controller.dart' hide ConnectionState;
import '../../core/process/supervisor.dart';
import '../../shared/widgets/glass.dart';

/// Connection settings: local spawn (desktop) vs remote attach.
class SettingsScreen extends ConsumerStatefulWidget {
  const SettingsScreen({super.key});

  @override
  ConsumerState<SettingsScreen> createState() => _SettingsScreenState();
}

class _SettingsScreenState extends ConsumerState<SettingsScreen> {
  late ConnectionSettings _draft;
  Future<String?>? _probe;
  String? _probedPath;

  @override
  void initState() {
    super.initState();
    _draft = ref.read(settingsProvider);
  }

  /// Memoized per path, so rebuilds don't spawn a probe process each time.
  Future<String?> _binaryProbe() {
    if (_probe == null || _probedPath != _draft.binaryPath) {
      _probedPath = _draft.binaryPath;
      _probe = _draft.binaryPath.isEmpty
          ? BinaryLocator.find()
          : BinaryLocator.verify(_draft.binaryPath);
    }
    return _probe!;
  }

  void _saveAndConnect() {
    ref.read(settingsProvider.notifier).update(_draft);
    unawaited(ref.read(connectionProvider.notifier).apply(_draft));
    context.go('/');
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final connection = ref.watch(connectionProvider);
    final (statusLabel, statusColor) = switch (connection.phase) {
      ConnectionPhase.connected => ('connected', GC.ok),
      ConnectionPhase.connecting => ('connecting', GC.warn),
      ConnectionPhase.error => ('error', GC.down),
      ConnectionPhase.disconnected => ('disconnected', GC.textDim),
    };

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
                    const Caption('Settings'),
                    const SizedBox(height: 6),
                    Text('Connection', style: theme.textTheme.headlineMedium),
                    const SizedBox(height: 8),
                    Text(
                      'Run gocode locally as a child process, or attach to a '
                      'server running elsewhere.',
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
                            selected: {_draft.mode},
                            showSelectedIcon: false,
                            onSelectionChanged: (s) => setState(
                              () => _draft = _draft.copyWith(mode: s.first),
                            ),
                          ),
                          const SizedBox(height: 22),
                          if (_draft.mode == ConnectionMode.local)
                            ..._localFields(theme)
                          else
                            ..._remoteFields(theme),
                          const SizedBox(height: 26),
                          Align(
                            alignment: Alignment.centerRight,
                            child: FilledButton.icon(
                              onPressed: _saveAndConnect,
                              icon: const Icon(Icons.bolt_rounded, size: 18),
                              label: const Text('Save & connect'),
                            ),
                          ),
                        ],
                      ),
                    ),
                    const SizedBox(height: 28),
                    const Caption('Status'),
                    const SizedBox(height: 10),
                    GlassSurface(
                      blur: false,
                      radius: GC.rCard,
                      padding: const EdgeInsets.all(20),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.stretch,
                        children: [
                          Row(
                            children: [
                              StatusPill(
                                label: statusLabel,
                                color: statusColor,
                              ),
                              const SizedBox(width: 12),
                              Expanded(
                                child: Text(
                                  connection.baseUrl ?? connection.error ?? '',
                                  maxLines: 3,
                                  overflow: TextOverflow.ellipsis,
                                  style: GC.code,
                                ),
                              ),
                              if (connection.isConnected)
                                TextButton(
                                  onPressed: () => ref
                                      .read(connectionProvider.notifier)
                                      .disconnect(),
                                  child: const Text('Disconnect'),
                                ),
                            ],
                          ),
                          if (connection.stderr.isNotEmpty) ...[
                            const SizedBox(height: 18),
                            const Caption('Server output'),
                            const SizedBox(height: 8),
                            CodeBlock(
                              connection.stderr.take(200).join('\n'),
                              maxHeight: 220,
                            ),
                          ],
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

  List<Widget> _localFields(ThemeData theme) => [
    _Field(
      label: 'gocode binary',
      hint: 'auto-detect from PATH',
      value: _draft.binaryPath,
      onChanged: (v) => setState(() => _draft = _draft.copyWith(binaryPath: v)),
    ),
    const SizedBox(height: 8),
    FutureBuilder<String?>(
      future: _binaryProbe(),
      builder: (context, snap) {
        if (snap.connectionState != ConnectionState.done) {
          return Text('looking for gocode…', style: theme.textTheme.bodySmall);
        }
        final found = snap.data;
        return Row(
          children: [
            Icon(
              found == null ? Icons.error_outline : Icons.check_circle_outline,
              size: 14,
              color: found == null ? GC.downText : GC.ok,
            ),
            const SizedBox(width: 6),
            Expanded(
              child: Text(
                found == null
                    ? 'not found — install gocode or set its path above'
                    : 'found: $found',
                style: theme.textTheme.bodySmall?.copyWith(
                  color: found == null ? GC.downText : GC.ok,
                ),
              ),
            ),
          ],
        );
      },
    ),
    const SizedBox(height: 18),
    _Field(
      label: 'Project directory',
      hint: 'the server runs here (one project per process)',
      value: _draft.workingDirectory,
      onChanged: (v) =>
          setState(() => _draft = _draft.copyWith(workingDirectory: v)),
      trailing: OutlinedButton(
        onPressed: () async {
          final directory = await FilePicker.getDirectoryPath();
          if (directory != null) {
            setState(
              () => _draft = _draft.copyWith(workingDirectory: directory),
            );
          }
        },
        child: const Text('Browse'),
      ),
    ),
  ];

  List<Widget> _remoteFields(ThemeData theme) => [
    _Field(
      label: 'Server URL',
      hint: 'http://host:port',
      value: _draft.remoteUrl,
      onChanged: (v) => setState(() => _draft = _draft.copyWith(remoteUrl: v)),
    ),
    const SizedBox(height: 16),
    _Field(
      label: 'Username (optional)',
      value: _draft.remoteUsername ?? '',
      onChanged: (v) =>
          setState(() => _draft = _draft.copyWith(remoteUsername: v)),
    ),
    const SizedBox(height: 16),
    _Field(
      label: 'Password (optional)',
      value: _draft.remotePassword ?? '',
      obscure: true,
      onChanged: (v) =>
          setState(() => _draft = _draft.copyWith(remotePassword: v)),
    ),
    const SizedBox(height: 10),
    Text(
      'Sent as basic auth. Only attach to servers on networks you trust.',
      style: theme.textTheme.bodySmall,
    ),
  ];
}

/// A labelled text field that owns its controller, so parent rebuilds don't
/// reset the cursor — and external changes (Browse) still flow in.
class _Field extends StatefulWidget {
  const _Field({
    required this.label,
    required this.value,
    required this.onChanged,
    this.hint,
    this.obscure = false,
    this.trailing,
  });

  final String label;
  final String value;
  final ValueChanged<String> onChanged;
  final String? hint;
  final bool obscure;
  final Widget? trailing;

  @override
  State<_Field> createState() => _FieldState();
}

class _FieldState extends State<_Field> {
  late final _controller = TextEditingController(text: widget.value);

  @override
  void didUpdateWidget(_Field oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.value != _controller.text) _controller.text = widget.value;
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Caption(widget.label),
        const SizedBox(height: 8),
        Row(
          children: [
            Expanded(
              child: TextField(
                controller: _controller,
                obscureText: widget.obscure,
                style: GC.code.copyWith(fontSize: 13.5, color: GC.textHi),
                decoration: InputDecoration(hintText: widget.hint),
                onChanged: widget.onChanged,
              ),
            ),
            if (widget.trailing != null) ...[
              const SizedBox(width: 8),
              widget.trailing!,
            ],
          ],
        ),
      ],
    );
  }
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:gpt_markdown/gpt_markdown.dart';

import '../../app/theme.dart';
import '../../core/api/harness_models.dart';
import '../../shared/widgets/glass.dart';
import 'harness_state.dart';
import 'harness_widgets.dart';

/// The experience library (documentation/11-desktop.md, Harness²): domains as
/// collapsible sections, entries as rows, an entry's lesson and harness
/// tree on tap, forget behind a confirm, sync on the cloud backend.
class HarnessLibraryScreen extends ConsumerStatefulWidget {
  const HarnessLibraryScreen({super.key});

  @override
  ConsumerState<HarnessLibraryScreen> createState() =>
      _HarnessLibraryScreenState();
}

class _HarnessLibraryScreenState extends ConsumerState<HarnessLibraryScreen> {
  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final available = ref.watch(harnessAvailableProvider);
    final libraryAsync = ref.watch(harnessLibraryProvider);

    return Scaffold(
      extendBodyBehindAppBar: true,
      appBar: PillHeader(
        leading: const HeaderBackButton(),
        title: Text('Experience library', style: theme.textTheme.titleSmall),
        actions: [
          if (available)
            libraryAsync.maybeWhen(
              data: (lib) => lib.backend == 'cloud'
                  ? TextButton.icon(
                      icon: const Icon(Icons.cloud_sync_outlined, size: 16),
                      label: const Text('Sync'),
                      onPressed: _sync,
                    )
                  : Tooltip(
                      message: lib.reason,
                      child: Padding(
                        padding: const EdgeInsets.symmetric(horizontal: 10),
                        child: Center(
                          child: StatusPill(
                            label: lib.backend,
                            color: GC.textDim,
                          ),
                        ),
                      ),
                    ),
              orElse: () => const SizedBox.shrink(),
            ),
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
              constraints: const BoxConstraints(maxWidth: 860),
              child: !available
                  ? const _NeedsServer()
                  : libraryAsync.when(
                      loading: () => const Center(
                        child: SizedBox.square(
                          dimension: 18,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        ),
                      ),
                      error: (e, _) => Center(
                        child: ErrorPanel(
                          message: '$e',
                          onRetry: () => ref.invalidate(harnessLibraryProvider),
                        ),
                      ),
                      data: (lib) => _list(context, lib),
                    ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _list(BuildContext context, HarnessLibrary lib) {
    if (lib.domains.isEmpty) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.school_outlined, size: 28, color: GC.textDim),
              const SizedBox(height: 12),
              Text(
                'No lessons yet',
                style: Theme.of(context).textTheme.titleSmall,
              ),
              const SizedBox(height: 6),
              const Text(
                'An entry is stored when a run adopts a harness and its '
                'record supports a lesson.',
                textAlign: TextAlign.center,
              ),
            ],
          ),
        ),
      );
    }
    final domains = lib.domains.keys.toList()..sort();
    return ListView(
      children: [for (final domain in domains) _DomainSection(domain: domain)],
    );
  }

  Future<void> _sync() async {
    final messenger = ScaffoldMessenger.of(context);
    try {
      final uploaded = await ref
          .read(harnessRunsProvider.notifier)
          .syncLibrary();
      messenger.showSnackBar(
        SnackBar(
          content: Text(
            '$uploaded entr${uploaded == 1 ? 'y' : 'ies'} uploaded',
          ),
        ),
      );
    } catch (e) {
      messenger.showSnackBar(SnackBar(content: Text('$e')));
    }
  }
}

class _DomainSection extends ConsumerWidget {
  const _DomainSection({required this.domain});

  final String domain;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final library = ref.watch(harnessLibraryProvider).value;
    final entries = library?.domains[domain] ?? const <HarnessEntryInfo>[];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        HarnessPhaseHeader(label: '$domain (${entries.length})'),
        for (final entry in entries)
          _EntryRow(
            domain: entry.domain,
            task: entry.task,
            cue: entry.pending ? 'pending upload · ${entry.cue}' : entry.cue,
            components: entry.components,
          ),
      ],
    );
  }
}

class _EntryRow extends ConsumerWidget {
  const _EntryRow({
    required this.domain,
    required this.task,
    required this.cue,
    required this.components,
  });

  final String domain;
  final String task;
  final String cue;
  final String components;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return Padding(
      padding: const EdgeInsets.only(left: 10, bottom: 6),
      child: InkWell(
        borderRadius: BorderRadius.circular(GC.rInput),
        onTap: () => _open(context, ref),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 8),
          child: Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(task, style: Theme.of(context).textTheme.titleSmall),
                    if (cue.isNotEmpty)
                      Text(
                        cue,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              Flexible(
                child: Text(
                  components,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: GC.code.copyWith(fontSize: 11.5, color: GC.textDim),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  void _open(BuildContext context, WidgetRef ref) {
    showHarnessEntrySheet(context, domain: domain, task: task);
  }
}

/// An entry's detail: lesson card, why adopted, what each edit did, and the
/// harness tree as an expandable file list.
Future<void> showHarnessEntrySheet(
  BuildContext context, {
  required String domain,
  required String task,
}) {
  final wide = MediaQuery.sizeOf(context).width >= 1100;
  if (wide) {
    return showGeneralDialog(
      context: context,
      barrierDismissible: true,
      barrierLabel: 'Close entry',
      barrierColor: const Color(0x66000000),
      transitionDuration: const Duration(milliseconds: 180),
      pageBuilder: (context, animation, _) => Material(
        // A general dialog has no Material of its own, and the sheet's
        // InkWells, buttons and tabs need one; transparent, so the glass
        // surface still shows.
        type: MaterialType.transparency,
        child: Align(
          alignment: Alignment.centerRight,
          child: _EntrySheet(domain: domain, task: task, width: 480),
        ),
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
      builder: (_) => _EntryPage(domain: domain, task: task),
    ),
  );
}

class _EntryPage extends StatelessWidget {
  const _EntryPage({required this.domain, required this.task});

  final String domain;
  final String task;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: PillHeader(
        leading: const HeaderBackButton(),
        title: Text(task, style: Theme.of(context).textTheme.titleSmall),
      ),
      body: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 14),
        child: _EntryBody(domain: domain, task: task),
      ),
    );
  }
}

class _EntrySheet extends StatelessWidget {
  const _EntrySheet({required this.domain, required this.task, this.width});

  final String domain;
  final String task;
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
              child: _EntryBody(domain: domain, task: task),
            ),
          ),
        ),
      ),
    );
  }
}

class _EntryBody extends ConsumerWidget {
  const _EntryBody({required this.domain, required this.task});

  final String domain;
  final String task;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final entryAsync = ref.watch(
      harnessEntryProvider((domain: domain, task: task)),
    );

    return entryAsync.when(
      loading: () => const Center(
        child: SizedBox.square(
          dimension: 18,
          child: CircularProgressIndicator(strokeWidth: 2),
        ),
      ),
      error: (e, _) => Padding(
        padding: const EdgeInsets.all(16),
        child: ErrorPanel(message: '$e'),
      ),
      data: (entry) => Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      '$domain / ${entry.task}',
                      style: theme.textTheme.titleSmall,
                    ),
                    Text(
                      'adopted ${entry.label}',
                      style: theme.textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
              TextButton(
                onPressed: () => _forget(context, ref),
                child: const Text('Forget'),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Expanded(
            child: ListView(
              children: [
                _lessonCard(context, entry),
                if (entry.why.isNotEmpty) ...[
                  const SizedBox(height: 14),
                  Caption('Why ${entry.label} was adopted'),
                  const SizedBox(height: 6),
                  GptMarkdown(
                    entry.why,
                    style: GC.reading.copyWith(color: GC.textBody),
                  ),
                ],
                if (entry.lesson.edits.isNotEmpty) ...[
                  const SizedBox(height: 14),
                  Caption('What each edit did'),
                  const SizedBox(height: 6),
                  CodeBlock(entry.lesson.edits),
                ],
                const SizedBox(height: 14),
                Caption('Harness'),
                const SizedBox(height: 6),
                for (final path in entry.paths)
                  if (entry.tree[path]!.trim().isNotEmpty)
                    _HarnessFile(path: path, content: entry.tree[path]!),
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _lessonCard(BuildContext context, HarnessEntry entry) {
    return GlassSurface(
      blur: false,
      padding: const EdgeInsets.all(14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _lessonLine(context, 'Cue', entry.lesson.cue),
          _lessonLine(context, 'Lesson', entry.lesson.lesson),
          _lessonLine(context, 'Evidence', entry.lesson.evidence),
          if (entry.lesson.confidence.isNotEmpty)
            _lessonLine(context, 'Confidence', entry.lesson.confidence),
        ],
      ),
    );
  }

  Widget _lessonLine(BuildContext context, String label, String value) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 6),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 90,
            child: Text(label, style: theme.textTheme.labelSmall),
          ),
          Expanded(child: Text(value, style: theme.textTheme.bodyMedium)),
        ],
      ),
    );
  }

  Future<void> _forget(BuildContext context, WidgetRef ref) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('Forget $task?'),
        content: const Text(
          'The entry — its lesson and the harness it recorded — is removed '
          'from the experience library.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            style: FilledButton.styleFrom(
              backgroundColor: GC.down,
              foregroundColor: GC.textHi,
            ),
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Forget'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;
    try {
      await ref.read(harnessRunsProvider.notifier).forgetEntry(domain, task);
      if (context.mounted) Navigator.maybePop(context);
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }
}

class _HarnessFile extends StatefulWidget {
  const _HarnessFile({required this.path, required this.content});

  final String path;
  final String content;

  @override
  State<_HarnessFile> createState() => _HarnessFileState();
}

class _HarnessFileState extends State<_HarnessFile> {
  bool _open = false;

  @override
  Widget build(BuildContext context) {
    final isMarkdown = widget.path.endsWith('.md');
    return Padding(
      padding: const EdgeInsets.only(bottom: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          InkWell(
            borderRadius: BorderRadius.circular(6),
            onTap: () => setState(() => _open = !_open),
            child: Row(
              children: [
                Icon(
                  _open
                      ? Icons.expand_more_rounded
                      : Icons.chevron_right_rounded,
                  size: 16,
                  color: GC.textDim,
                ),
                const SizedBox(width: 4),
                Expanded(
                  child: Text(
                    widget.path,
                    style: GC.code.copyWith(fontSize: 12, color: GC.textHi),
                  ),
                ),
              ],
            ),
          ),
          if (_open)
            Padding(
              padding: const EdgeInsets.only(left: 20, top: 4),
              child: isMarkdown
                  ? GptMarkdown(
                      widget.content,
                      style: GC.reading.copyWith(color: GC.textBody),
                    )
                  : CodeBlock(widget.content, maxHeight: 320),
            ),
        ],
      ),
    );
  }
}

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
            const Icon(Icons.school_outlined, size: 28, color: GC.textDim),
            const SizedBox(height: 12),
            Text(
              'The experience library needs Server or Remote mode',
              style: theme.textTheme.titleSmall,
            ),
            const SizedBox(height: 6),
            const Text(
              'ACP mode has no HTTP API. Switch the connection mode in '
              'Settings.',
              textAlign: TextAlign.center,
            ),
          ],
        ),
      ),
    );
  }
}

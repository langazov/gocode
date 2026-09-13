import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import '../../core/connection/controller.dart';
import '../../shared/widgets/glass.dart';
import 'providers.dart';

/// Home: the sessions this server knows, newest first.
class HomeScreen extends ConsumerStatefulWidget {
  const HomeScreen({super.key});

  @override
  ConsumerState<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends ConsumerState<HomeScreen> {
  final _search = TextEditingController();
  String _query = '';

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  Future<void> _refresh() async {
    // sessionsProvider is autoDispose; invalidate to re-fetch.
    ref.invalidate(sessionsProvider);
  }

  @override
  Widget build(BuildContext context) {
    final sessions = ref.watch(sessionsProvider);
    final connection = ref.watch(connectionProvider);
    final settings = ref.watch(settingsProvider);
    final theme = Theme.of(context);
    final where = settings.mode == ConnectionMode.local
        ? settings.workingDirectory
        : (connection.baseUrl ?? settings.remoteUrl);

    return Scaffold(
      extendBodyBehindAppBar: true,
      appBar: PillHeader(
        actions: [
          IconButton(
            tooltip: 'Refresh',
            icon: const Icon(Icons.refresh_rounded),
            onPressed: _refresh,
          ),
          IconButton(
            tooltip: 'Settings',
            icon: const Icon(Icons.tune_rounded),
            onPressed: () => context.push('/settings'),
          ),
          const SizedBox(width: 6),
          FilledButton.icon(
            style: FilledButton.styleFrom(
              padding: const EdgeInsets.symmetric(horizontal: 20),
            ),
            onPressed: () => context.push('/new'),
            icon: const Icon(Icons.add_rounded, size: 18),
            label: const Text('New session'),
          ),
        ],
      ),
      // Built inside the Scaffold: only there do the insets include the
      // floating header the body extends behind.
      body: Builder(
        builder: (context) => RefreshIndicator(
          onRefresh: _refresh,
          edgeOffset: MediaQuery.paddingOf(context).top,
          child: ListView(
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
                  constraints: const BoxConstraints(maxWidth: 880),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      const Caption('Workspace'),
                      const SizedBox(height: 6),
                      Text('Sessions', style: theme.textTheme.headlineMedium),
                      if (where.isNotEmpty) ...[
                        const SizedBox(height: 6),
                        Text(
                          where,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: GC.code.copyWith(color: GC.textFaint),
                        ),
                      ],
                      const SizedBox(height: 22),
                      TextField(
                        controller: _search,
                        onChanged: (v) =>
                            setState(() => _query = v.trim().toLowerCase()),
                        decoration: const InputDecoration(
                          hintText: 'Search sessions…',
                          prefixIcon: Icon(Icons.search, size: 18),
                        ),
                      ),
                      const SizedBox(height: 16),
                      sessions.when(
                        loading: () => const Padding(
                          padding: EdgeInsets.all(48),
                          child: Center(
                            child: CircularProgressIndicator(strokeWidth: 2),
                          ),
                        ),
                        error: (e, _) =>
                            ErrorPanel(message: '$e', onRetry: _refresh),
                        data: (list) => _SessionList(
                          sessions: list,
                          query: _query,
                          onChanged: _refresh,
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _SessionList extends StatelessWidget {
  const _SessionList({
    required this.sessions,
    required this.query,
    required this.onChanged,
  });

  final List<Session> sessions;
  final String query;
  final Future<void> Function() onChanged;

  @override
  Widget build(BuildContext context) {
    final filtered =
        sessions
            .where(
              (s) =>
                  query.isEmpty ||
                  s.title.toLowerCase().contains(query) ||
                  s.directory.toLowerCase().contains(query),
            )
            .toList()
          ..sort((a, b) => b.timeUpdated.compareTo(a.timeUpdated));
    if (filtered.isEmpty) return _EmptyHome(searching: query.isNotEmpty);
    return Column(
      children: [
        for (final s in filtered)
          Padding(
            padding: const EdgeInsets.only(bottom: 10),
            child: _SessionTile(session: s, onChanged: onChanged),
          ),
      ],
    );
  }
}

class _EmptyHome extends StatelessWidget {
  const _EmptyHome({required this.searching});

  final bool searching;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return GlassSurface(
      blur: false,
      radius: GC.rCard,
      padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 40),
      child: Column(
        children: [
          const Icon(Icons.terminal_rounded, size: 36, color: GC.accentText),
          const SizedBox(height: 14),
          Text(
            searching ? 'No sessions match' : 'No sessions yet',
            style: theme.textTheme.titleLarge,
          ),
          const SizedBox(height: 6),
          Text(
            searching
                ? 'Try a different title or folder name.'
                : 'Start one and gocode gets to work in this project.',
            textAlign: TextAlign.center,
            style: theme.textTheme.bodyMedium,
          ),
          if (!searching) ...[
            const SizedBox(height: 22),
            FilledButton.icon(
              onPressed: () => context.push('/new'),
              icon: const Icon(Icons.add_rounded, size: 18),
              label: const Text('Start a session'),
            ),
          ],
        ],
      ),
    );
  }
}

class _SessionTile extends ConsumerWidget {
  const _SessionTile({required this.session, required this.onChanged});

  final Session session;
  final Future<void> Function() onChanged;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final client = ref.watch(apiClientProvider);
    final meta = [
      session.directory.split('/').last,
      ?session.model?.id,
      _relative(session.timeUpdated),
    ].where((s) => s.isNotEmpty).join(' · ');

    return GlassSurface(
      blur: false,
      shadow: false,
      radius: GC.rItem,
      padding: const EdgeInsets.fromLTRB(14, 12, 6, 12),
      onTap: () => context.push('/session/${session.id}'),
      child: Row(
        children: [
          Container(
            width: 38,
            height: 38,
            decoration: BoxDecoration(
              color: GC.accent.withValues(alpha: 0.12),
              borderRadius: BorderRadius.circular(GC.rInput),
              border: Border.all(color: GC.accent.withValues(alpha: 0.22)),
            ),
            child: Icon(
              session.isSubagent
                  ? Icons.subdirectory_arrow_right_rounded
                  : switch (session.agent) {
                      'plan' => Icons.account_tree_outlined,
                      'general-purpose' => Icons.smart_toy_outlined,
                      _ => Icons.chat_bubble_outline_rounded,
                    },
              size: 18,
              color: GC.accentText,
            ),
          ),
          const SizedBox(width: 14),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(
                  session.title.isEmpty ? 'Untitled' : session.title,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.titleSmall,
                ),
                const SizedBox(height: 2),
                Text(
                  meta,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodySmall,
                ),
              ],
            ),
          ),
          PopupMenuButton<String>(
            tooltip: 'Session actions',
            icon: const Icon(Icons.more_horiz_rounded, color: GC.textDim),
            onSelected: (action) async {
              if (action == 'open') {
                context.push('/session/${session.id}');
              } else if (action == 'rename') {
                final title = await _promptTitle(context, session.title);
                if (title != null && title.isNotEmpty) {
                  await client?.renameSession(session.id, title);
                  await onChanged();
                }
              } else if (action == 'fork') {
                await client?.forkSession(session.id);
                await onChanged();
              } else if (action == 'delete') {
                final ok = await _confirmDelete(context, session.title);
                if (ok == true) {
                  await client?.deleteSession(session.id);
                  await onChanged();
                }
              }
            },
            itemBuilder: (_) => const [
              PopupMenuItem(value: 'open', child: Text('Open')),
              PopupMenuItem(value: 'rename', child: Text('Rename')),
              PopupMenuItem(value: 'fork', child: Text('Fork')),
              PopupMenuItem(value: 'delete', child: Text('Delete')),
            ],
          ),
        ],
      ),
    );
  }

  String _relative(int millis) {
    if (millis == 0) return '';
    final age = DateTime.now().difference(
      DateTime.fromMillisecondsSinceEpoch(millis),
    );
    if (age.inMinutes < 1) return 'now';
    if (age.inHours < 1) return '${age.inMinutes}m ago';
    if (age.inDays < 1) return '${age.inHours}h ago';
    if (age.inDays < 30) return '${age.inDays}d ago';
    return '${age.inDays ~/ 30}mo ago';
  }

  Future<String?> _promptTitle(BuildContext context, String current) {
    final controller = TextEditingController(text: current);
    return showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Rename session'),
        content: SizedBox(
          width: 420,
          child: TextField(
            controller: controller,
            autofocus: true,
            onSubmitted: (v) => Navigator.pop(context, v),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, controller.text),
            child: const Text('Save'),
          ),
        ],
      ),
    );
  }

  Future<bool?> _confirmDelete(BuildContext context, String title) {
    return showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Delete session?'),
        content: Text(
          '"${title.isEmpty ? "Untitled" : title}" will be removed permanently.',
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
            child: const Text('Delete'),
          ),
        ],
      ),
    );
  }
}

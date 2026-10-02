import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/api/git_models.dart';
import 'diff_view.dart';
import 'git_state.dart';
import 'git_widgets.dart';
import 'graph.dart';

// Ported from goide (app/lib/git/history_panel.dart).

const _rowH = 28.0;
const _laneW = 14.0;

/// Optional path filter for the history view (file history).
class HistoryPathNotifier extends Notifier<String> {
  @override
  String build() => '';

  void set(String path) => state = path;
}

final historyPathProvider = NotifierProvider<HistoryPathNotifier, String>(
  HistoryPathNotifier.new,
);

/// Commit history with a branch graph, search, details and commit actions.
class GitHistoryView extends ConsumerStatefulWidget {
  const GitHistoryView({super.key});

  @override
  ConsumerState<GitHistoryView> createState() => _GitHistoryViewState();
}

class _GitHistoryViewState extends ConsumerState<GitHistoryView> {
  static const _page = 200;

  final _query = TextEditingController();
  final _scroll = ScrollController();
  Timer? _debounce;

  List<GitCommitInfo> _commits = [];
  List<GraphRow> _graph = [];
  bool _all = true;
  bool _loading = false;
  bool _hasMore = true;
  String? _error;
  String? _selected;
  String _lastHead = '';

  @override
  void initState() {
    super.initState();
    _scroll.addListener(() {
      if (_hasMore && !_loading && _scroll.position.extentAfter < 400) {
        _load(more: true);
      }
    });
  }

  @override
  void dispose() {
    _debounce?.cancel();
    _query.dispose();
    _scroll.dispose();
    super.dispose();
  }

  Future<void> _load({bool more = false}) async {
    if (_loading) return;
    final dir = ref.read(gitDirectoryProvider);
    setState(() => _loading = true);
    // Another folder by the time the page arrives: drop it, load again.
    bool stale() => ref.read(gitDirectoryProvider) != dir;
    try {
      final page = await ref
          .read(gitProvider.notifier)
          .log(
            limit: _page,
            skip: more ? _commits.length : 0,
            all: _all,
            query: _query.text.trim(),
            path: ref.read(historyPathProvider),
          );
      if (!mounted) return;
      if (stale()) {
        setState(() => _loading = false);
        await _load();
        return;
      }
      final commits = more ? [..._commits, ...page] : page;
      setState(() {
        _commits = commits;
        _graph = layoutGraph([for (final c in commits) (c.hash, c.parents)]);
        _hasMore = page.length == _page;
        _error = null;
        _loading = false;
      });
    } catch (e) {
      if (mounted) {
        setState(() {
          _error = stale() ? null : '$e';
          _loading = false;
        });
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    // Reload when HEAD/branch state changes (commits, checkouts, pulls).
    final git = ref.watch(gitProvider);
    final pathFilter = ref.watch(historyPathProvider);
    final dir = ref.watch(gitDirectoryProvider);
    final headKey =
        '$dir|${git.status?.head}|${git.branch}|${git.ahead}|${git.behind}|$pathFilter';
    if (headKey != _lastHead) {
      final otherFolder = !_lastHead.startsWith('$dir|');
      _lastHead = headKey;
      if (otherFolder) {
        _commits = [];
        _graph = [];
        _selected = null;
        _error = null;
        _hasMore = true;
      }
      if (git.isRepo) Future.microtask(_load);
    }

    if (git.status != null && !git.isRepo) {
      return const Center(child: Text('Not a git repository'));
    }

    final selected = _commits.where((c) => c.hash == _selected).firstOrNull;
    final maxLanes = _graph.isEmpty
        ? 1
        : math.min(12, _graph.map((r) => r.width).reduce(math.max));

    final list = Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _toolbar(pathFilter),
        if (_loading && _commits.isEmpty)
          const LinearProgressIndicator(minHeight: 2),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.all(10),
            child: SelectableText(
              _error!,
              style: const TextStyle(color: GC.downText, fontSize: 12),
            ),
          ),
        Expanded(
          child: _commits.isEmpty && !_loading
              ? const Center(child: Text('No commits'))
              : ListView.builder(
                  controller: _scroll,
                  itemExtent: _rowH,
                  itemCount: _commits.length + (_hasMore ? 1 : 0),
                  itemBuilder: (context, i) {
                    if (i >= _commits.length) {
                      return const Center(
                        child: SizedBox(
                          width: 14,
                          height: 14,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        ),
                      );
                    }
                    final c = _commits[i];
                    return _CommitRow(
                      commit: c,
                      graph: _graph[i],
                      lanes: maxLanes,
                      selected: c.hash == _selected,
                      onTap: () => setState(
                        () => _selected = _selected == c.hash ? null : c.hash,
                      ),
                      onMenu: (pos) => _commitMenu(c, pos),
                    );
                  },
                ),
        ),
      ],
    );

    if (selected == null) return list;
    return LayoutBuilder(
      builder: (context, c) {
        final details = _CommitDetails(
          key: ValueKey(selected.hash),
          commit: selected,
          onClose: () => setState(() => _selected = null),
        );
        if (c.maxWidth > 760) {
          return Row(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Expanded(child: list),
              const VerticalDivider(width: 1),
              SizedBox(width: math.min(460, c.maxWidth * 0.42), child: details),
            ],
          );
        }
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Expanded(child: list),
            const Divider(height: 1),
            SizedBox(height: math.min(300, c.maxHeight * 0.5), child: details),
          ],
        );
      },
    );
  }

  Widget _toolbar(String pathFilter) => Padding(
    padding: const EdgeInsets.fromLTRB(8, 6, 6, 6),
    child: Row(
      children: [
        Expanded(
          child: SizedBox(
            height: 34,
            child: TextField(
              controller: _query,
              style: const TextStyle(fontSize: 12.5),
              decoration: const InputDecoration(
                hintText: 'Search commit messages…',
                prefixIcon: Icon(Icons.search_rounded, size: 15),
                prefixIconConstraints: BoxConstraints(minWidth: 30),
                contentPadding: EdgeInsets.symmetric(
                  horizontal: 8,
                  vertical: 6,
                ),
              ),
              onChanged: (_) {
                _debounce?.cancel();
                _debounce = Timer(const Duration(milliseconds: 300), _load);
              },
            ),
          ),
        ),
        if (pathFilter.isNotEmpty) ...[
          const SizedBox(width: 6),
          InputChip(
            label: Text(baseName(pathFilter)),
            avatar: const Icon(Icons.description_outlined, size: 14),
            onDeleted: () => ref.read(historyPathProvider.notifier).set(''),
            visualDensity: VisualDensity.compact,
          ),
        ],
        const SizedBox(width: 6),
        FilterChip(
          label: const Text('All branches'),
          selected: _all,
          visualDensity: VisualDensity.compact,
          onSelected: (v) {
            setState(() => _all = v);
            _load();
          },
        ),
        const SizedBox(width: 4),
        GitIconButton(
          tooltip: 'Refresh',
          size: 34,
          onPressed: _load,
          icon: Icons.refresh_rounded,
        ),
      ],
    ),
  );

  Future<void> _commitMenu(GitCommitInfo c, Offset pos) async {
    final overlay = Overlay.of(context).context.findRenderObject() as RenderBox;
    PopupMenuItem<String> item(
      String v,
      IconData icon,
      String label, {
      bool danger = false,
    }) => PopupMenuItem(
      value: v,
      height: 34,
      child: Row(
        children: [
          Icon(icon, size: 15, color: danger ? GC.downText : GC.textDim),
          const SizedBox(width: 10),
          Text(label, style: TextStyle(color: danger ? GC.downText : null)),
        ],
      ),
    );
    final choice = await showMenu<String>(
      context: context,
      position: RelativeRect.fromRect(
        pos & const Size(1, 1),
        Offset.zero & overlay.size,
      ),
      items: [
        item('copy', Icons.copy_rounded, 'Copy Commit Hash'),
        item('checkout', Icons.login_rounded, 'Checkout (detached)'),
        item('branch', Icons.call_split_rounded, 'Create Branch Here…'),
        item('tag', Icons.sell_outlined, 'Create Tag…'),
        const PopupMenuDivider(height: 8),
        item('cherry-pick', Icons.colorize_outlined, 'Cherry-pick'),
        item('revert', Icons.undo_rounded, 'Revert Commit'),
        const PopupMenuDivider(height: 8),
        item('reset-soft', Icons.restore_rounded, 'Reset Branch Here (soft)'),
        item('reset-mixed', Icons.restore_rounded, 'Reset Branch Here (mixed)'),
        item(
          'reset-hard',
          Icons.warning_amber_rounded,
          'Reset Branch Here (hard)…',
          danger: true,
        ),
      ],
    );
    if (choice == null || !mounted) return;
    final git = ref.read(gitProvider.notifier);
    switch (choice) {
      case 'copy':
        await Clipboard.setData(ClipboardData(text: c.hash));
      case 'checkout':
        await git.checkout(c.hash);
      case 'branch':
        final name = await promptText(
          context,
          title: 'New branch at ${c.shortHash}',
          action: 'Create',
        );
        if (name != null && name.isNotEmpty) {
          await git.createBranch(name, startPoint: c.hash);
        }
      case 'tag':
        final name = await promptText(
          context,
          title: 'Tag ${c.shortHash}',
          hint: 'v1.2.3',
          action: 'Tag',
        );
        if (name != null && name.isNotEmpty) {
          await git.commitOp('tag', c.hash, name: name);
        }
      case 'reset-hard':
        if (await confirmDanger(
          context,
          title: 'Hard reset to ${c.shortHash}?',
          message: 'All uncommitted changes and commits after it on this branch will be lost.',
          action: 'Reset',
        )) {
          await git.commitOp(choice, c.hash);
        }
      default:
        await git.commitOp(choice, c.hash);
    }
  }
}

class _CommitRow extends StatefulWidget {
  const _CommitRow({
    required this.commit,
    required this.graph,
    required this.lanes,
    required this.selected,
    required this.onTap,
    required this.onMenu,
  });

  final GitCommitInfo commit;
  final GraphRow graph;
  final int lanes;
  final bool selected;
  final VoidCallback onTap;
  final void Function(Offset globalPos) onMenu;

  @override
  State<_CommitRow> createState() => _CommitRowState();
}

class _CommitRowState extends State<_CommitRow> {
  bool _hover = false;

  @override
  Widget build(BuildContext context) {
    final c = widget.commit;
    const meta = TextStyle(
      fontFamily: GC.sans,
      fontSize: 11.5,
      color: GC.textDim,
    );
    return MouseRegion(
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() => _hover = false),
      child: GestureDetector(
        onSecondaryTapDown: (d) => widget.onMenu(d.globalPosition),
        onLongPressStart: (d) => widget.onMenu(d.globalPosition),
        child: InkWell(
          onTap: widget.onTap,
          child: Container(
            color: widget.selected
                ? GC.accent.withValues(alpha: 0.12)
                : (_hover ? GC.surface3 : null),
            padding: const EdgeInsets.only(left: 6, right: 8),
            // Narrow windows drop columns (author, then time/hash) and
            // badges instead of overflowing.
            child: LayoutBuilder(
              builder: (context, box) {
                final w = box.maxWidth;
                final graphW = widget.lanes * _laneW + 6;
                return Row(
                  children: [
                    CustomPaint(
                      size: Size(graphW, _rowH),
                      painter: _GraphPainter(
                        widget.graph,
                        c.hash,
                        c.parents.length > 1,
                      ),
                    ),
                    Expanded(
                      child: Row(
                        children: [
                          for (final r in c.refs.take(
                            w > 520
                                ? 4
                                : w > 320
                                ? 2
                                : 0,
                          ))
                            Flexible(child: _RefBadge(r)),
                          Flexible(
                            flex: 3,
                            fit: FlexFit.tight,
                            child: Text(
                              c.subject,
                              overflow: TextOverflow.ellipsis,
                              style: const TextStyle(
                                fontFamily: GC.sans,
                                fontSize: 12.5,
                                color: GC.textHi,
                              ),
                            ),
                          ),
                        ],
                      ),
                    ),
                    if (w > 560) ...[
                      const SizedBox(width: 8),
                      ConstrainedBox(
                        constraints: const BoxConstraints(maxWidth: 120),
                        child: Text(
                          c.author,
                          overflow: TextOverflow.ellipsis,
                          style: meta,
                        ),
                      ),
                    ],
                    if (w > 380) ...[
                      const SizedBox(width: 8),
                      SizedBox(
                        width: 58,
                        child: Text(
                          relativeTime(c.time),
                          textAlign: TextAlign.right,
                          style: meta.copyWith(
                            fontSize: 11,
                            color: GC.textFaint,
                          ),
                        ),
                      ),
                      const SizedBox(width: 8),
                      Text(
                        c.shortHash,
                        style: GC.code.copyWith(
                          fontSize: 11,
                          color: GC.textFaint,
                        ),
                      ),
                    ],
                  ],
                );
              },
            ),
          ),
        ),
      ),
    );
  }
}

class _RefBadge extends StatelessWidget {
  const _RefBadge(this.ref);

  final String ref;

  @override
  Widget build(BuildContext context) {
    final head = ref.startsWith('HEAD -> ') || ref == 'HEAD';
    final tag = ref.startsWith('tag: ');
    final remote = !head && !tag && ref.contains('/');
    final label = ref.replaceFirst('HEAD -> ', '').replaceFirst('tag: ', '');
    final color = head
        ? GC.accent
        : tag
        ? GC.warn
        : remote
        ? GitColors.remoteRef
        : GC.ok;
    return Container(
      margin: const EdgeInsets.only(right: 5),
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.14),
        borderRadius: BorderRadius.circular(9),
        border: Border.all(color: color.withValues(alpha: 0.5)),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(
            tag
                ? Icons.sell_outlined
                : remote
                ? Icons.cloud_outlined
                : Icons.account_tree_outlined,
            size: 10,
            color: color,
          ),
          const SizedBox(width: 3),
          Flexible(
            child: Text(
              label,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontFamily: GC.sans,
                fontSize: 10.5,
                color: color,
                fontWeight: head ? FontWeight.w700 : FontWeight.w500,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _GraphPainter extends CustomPainter {
  _GraphPainter(this.row, this.hash, this.merge);

  final GraphRow row;
  final String hash;
  final bool merge;

  double _x(int lane) => 6 + lane * _laneW + _laneW / 2;

  void _edge(Canvas canvas, Offset a, Offset b, Color color) {
    final p = Paint()
      ..color = color
      ..strokeWidth = 1.6
      ..style = PaintingStyle.stroke;
    if (a.dx == b.dx) {
      canvas.drawLine(a, b, p);
      return;
    }
    final midY = (a.dy + b.dy) / 2;
    canvas.drawPath(
      Path()
        ..moveTo(a.dx, a.dy)
        ..cubicTo(a.dx, midY, b.dx, midY, b.dx, b.dy),
      p,
    );
  }

  @override
  void paint(Canvas canvas, Size size) {
    final h = size.height;
    final node = Offset(_x(row.column), h / 2);

    // Lanes entering from above: into the node, straight through, or bending.
    for (var j = 0; j < row.lanesIn.length; j++) {
      final want = row.lanesIn[j];
      if (want == null) continue;
      if (want == hash) {
        _edge(canvas, Offset(_x(j), 0), node, GitColors.lane(j));
        continue;
      }
      final k = j < row.lanesOut.length && row.lanesOut[j] == want
          ? j
          : row.lanesOut.indexOf(want);
      if (k < 0) continue;
      _edge(canvas, Offset(_x(j), 0), Offset(_x(k), h), GitColors.lane(k));
    }
    // Node to its parents below.
    for (final k in row.parentLanes) {
      _edge(canvas, node, Offset(_x(k), h), GitColors.lane(k));
    }
    canvas.drawCircle(node, 4.2, Paint()..color = GC.bgPage);
    canvas.drawCircle(
      node,
      merge ? 3.4 : 3.8,
      Paint()
        ..color = GitColors.lane(row.column)
        ..style = merge ? PaintingStyle.stroke : PaintingStyle.fill
        ..strokeWidth = 1.8,
    );
  }

  @override
  bool shouldRepaint(_GraphPainter old) =>
      old.row != row || old.hash != hash || old.merge != merge;
}

class _CommitDetails extends ConsumerStatefulWidget {
  const _CommitDetails({
    super.key,
    required this.commit,
    required this.onClose,
  });

  final GitCommitInfo commit;
  final VoidCallback onClose;

  @override
  ConsumerState<_CommitDetails> createState() => _CommitDetailsState();
}

class _CommitDetailsState extends ConsumerState<_CommitDetails> {
  GitCommitDetails? _show;
  String? _error;

  @override
  void initState() {
    super.initState();
    ref
        .read(gitProvider.notifier)
        .show(widget.commit.hash)
        .then(
          (s) {
            if (mounted) setState(() => _show = s);
          },
          onError: (Object e) {
            if (mounted) setState(() => _error = '$e');
          },
        );
  }

  @override
  Widget build(BuildContext context) {
    final c = widget.commit;
    final s = _show;
    final date = DateTime.fromMillisecondsSinceEpoch(c.time * 1000);
    const muted = TextStyle(
      fontFamily: GC.sans,
      fontSize: 12,
      color: GC.textDim,
    );
    return ColoredBox(
      color: GC.surface1,
      child: SelectionArea(
        child: ListView(
          padding: const EdgeInsets.fromLTRB(14, 10, 8, 14),
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    c.subject,
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                ),
                IconButton(
                  tooltip: 'Close',
                  onPressed: widget.onClose,
                  icon: const Icon(Icons.close_rounded, size: 16),
                ),
              ],
            ),
            const SizedBox(height: 6),
            Text('${c.author} <${c.email}>', style: muted),
            Text(
              '${date.year}-${_pad(date.month)}-${_pad(date.day)} '
              '${_pad(date.hour)}:${_pad(date.minute)}'
              '  ·  ${relativeTime(c.time)}',
              style: muted,
            ),
            const SizedBox(height: 4),
            Text(
              '${c.hash}${c.parents.isEmpty ? '' : '\nparents: ${c.parents.map((p) => p.length > 7 ? p.substring(0, 7) : p).join(', ')}'}',
              style: GC.code.copyWith(fontSize: 11.5, color: GC.textFaint),
            ),
            if (_error != null)
              Padding(
                padding: const EdgeInsets.only(top: 12),
                child: Text(
                  _error!,
                  style: const TextStyle(color: GC.downText),
                ),
              )
            else if (s != null) ...[
              if (s.body.trim() != c.subject.trim()) ...[
                const SizedBox(height: 10),
                Text(
                  s.body
                      .substring(math.min(s.body.length, c.subject.length))
                      .trim(),
                  style: GC.reading.copyWith(fontSize: 12.5),
                ),
              ],
              const SizedBox(height: 12),
              Text(
                '${s.files.length} file${s.files.length == 1 ? '' : 's'} changed',
                style: muted.copyWith(fontSize: 11.5),
              ),
              Text.rich(
                TextSpan(
                  children: [
                    TextSpan(
                      text: '+${s.additions} ',
                      style: const TextStyle(color: GitColors.addedFg),
                    ),
                    TextSpan(
                      text: '−${s.deletions}',
                      style: const TextStyle(color: GitColors.removedFg),
                    ),
                  ],
                  style: GC.code.copyWith(fontSize: 11.5),
                ),
              ),
              const SizedBox(height: 6),
              for (final f in s.files)
                InkWell(
                  onTap: () => showGitDiff(
                    context,
                    path: f.path,
                    source: DiffSource.commit,
                    commit: c.hash,
                  ),
                  child: Padding(
                    padding: const EdgeInsets.symmetric(vertical: 4),
                    child: Row(
                      children: [
                        StatusLetter(f.status),
                        const SizedBox(width: 8),
                        Expanded(
                          child: Text(
                            f.origPath.isNotEmpty
                                ? '${f.origPath} → ${f.path}'
                                : f.path,
                            overflow: TextOverflow.ellipsis,
                            style: const TextStyle(
                              fontFamily: GC.sans,
                              fontSize: 12.5,
                              color: GC.textHi,
                            ),
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
            ] else
              const Padding(
                padding: EdgeInsets.all(16),
                child: Center(
                  child: SizedBox(
                    width: 16,
                    height: 16,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }

  static String _pad(int v) => v.toString().padLeft(2, '0');
}

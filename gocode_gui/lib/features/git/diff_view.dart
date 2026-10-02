import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import 'diff_model.dart';
import 'git_state.dart';
import 'git_widgets.dart';

// Ported from goide (app/lib/git/diff_view.dart): inline and side-by-side
// views, hunk navigation (⌥↑/⌥↓, ⌥N/⌥P), and stage / unstage / discard per
// hunk and per file. Divergence: no "Open File" — gocode_gui has no editor.

/// Which version of a file a diff compares.
enum DiffSource { working, staged, untracked, commit }

/// Opens the diff viewer for [path] (repo-relative).
Future<void> showGitDiff(
  BuildContext context, {
  required String path,
  required DiffSource source,
  String commit = '',
}) {
  return showDialog<void>(
    context: context,
    builder: (_) => GitDiffDialog(path: path, source: source, commit: commit),
  );
}

class GitDiffDialog extends ConsumerStatefulWidget {
  const GitDiffDialog({
    super.key,
    required this.path,
    required this.source,
    this.commit = '',
  });

  final String path;
  final DiffSource source;
  final String commit;

  @override
  ConsumerState<GitDiffDialog> createState() => _GitDiffDialogState();
}

class _GitDiffDialogState extends ConsumerState<GitDiffDialog> {
  FileDiff? _diff;
  String? _error;
  bool _split = false;
  final _hunkKeys = <GlobalKey>[];
  final _scroll = ScrollController();
  int _currentHunk = 0;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  GitNotifier get _git => ref.read(gitProvider.notifier);

  Future<void> _load() async {
    try {
      final text = await _git.diff(
        widget.path,
        staged: widget.source == DiffSource.staged,
        untracked: widget.source == DiffSource.untracked,
        commit: widget.source == DiffSource.commit ? widget.commit : '',
      );
      if (!mounted) return;
      final d = parseDiff(text);
      setState(() {
        _diff = d;
        _error = null;
        _hunkKeys
          ..clear()
          ..addAll(List.generate(d.hunks.length, (_) => GlobalKey()));
        _currentHunk = _currentHunk.clamp(
          0,
          d.hunks.isEmpty ? 0 : d.hunks.length - 1,
        );
      });
    } catch (e) {
      if (mounted) setState(() => _error = '$e');
    }
  }

  void _gotoHunk(int i) {
    if (i < 0 || i >= _hunkKeys.length) return;
    final ctx = _hunkKeys[i].currentContext;
    if (ctx != null) {
      Scrollable.ensureVisible(
        ctx,
        duration: const Duration(milliseconds: 180),
        alignment: 0.05,
      );
    }
    setState(() => _currentHunk = i);
  }

  Future<void> _hunkAction(
    DiffHunk hunk, {
    required bool cached,
    required bool reverse,
  }) async {
    final ok = await _git.applyPatch(
      _diff!.patchFor(hunk),
      cached: cached,
      reverse: reverse,
    );
    if (ok) await _load();
  }

  Future<void> _fileAction(Future<bool> Function() action) async {
    if (await action() && mounted) Navigator.pop(context);
  }

  @override
  Widget build(BuildContext context) {
    final size = MediaQuery.sizeOf(context);
    final d = _diff;
    final dir = dirName(widget.path);
    final sourceLabel = switch (widget.source) {
      DiffSource.working => 'Working Tree',
      DiffSource.staged => 'Staged',
      DiffSource.untracked => 'Untracked',
      DiffSource.commit =>
        widget.commit.length > 7
            ? widget.commit.substring(0, 7)
            : widget.commit,
    };
    final notice = ref.watch(gitProvider.select((g) => g.notice));

    return Dialog(
      // Opaque: code must not have the page bleeding through behind it.
      backgroundColor: GC.surface1,
      insetPadding: EdgeInsets.all(size.width < 700 ? 6 : 28),
      clipBehavior: Clip.antiAlias,
      child: CallbackShortcuts(
        bindings: {
          const SingleActivator(LogicalKeyboardKey.keyN, alt: true): () =>
              _gotoHunk(_currentHunk + 1),
          const SingleActivator(LogicalKeyboardKey.keyP, alt: true): () =>
              _gotoHunk(_currentHunk - 1),
          const SingleActivator(LogicalKeyboardKey.arrowDown, alt: true): () =>
              _gotoHunk(_currentHunk + 1),
          const SingleActivator(LogicalKeyboardKey.arrowUp, alt: true): () =>
              _gotoHunk(_currentHunk - 1),
        },
        child: Focus(
          autofocus: true,
          child: SizedBox(
            width: 1400,
            height: size.height,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                // Title + actions.
                Container(
                  padding: const EdgeInsets.fromLTRB(16, 10, 8, 10),
                  color: GC.surface2,
                  child: Wrap(
                    crossAxisAlignment: WrapCrossAlignment.center,
                    spacing: 8,
                    runSpacing: 6,
                    children: [
                      const Icon(
                        Icons.difference_outlined,
                        size: 17,
                        color: GC.accent,
                      ),
                      Text(
                        baseName(widget.path),
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                      if (dir.isNotEmpty)
                        Text(
                          dir,
                          style: const TextStyle(
                            fontFamily: GC.sans,
                            fontSize: 12,
                            color: GC.textFaint,
                          ),
                        ),
                      GitChip(sourceLabel),
                      if (d != null && !d.isEmpty) ...[
                        Text(
                          '+${d.additions}',
                          style: GC.code.copyWith(color: GitColors.addedFg),
                        ),
                        Text(
                          '−${d.deletions}',
                          style: GC.code.copyWith(color: GitColors.removedFg),
                        ),
                      ],
                      const SizedBox(width: 12),
                      SegmentedButton<bool>(
                        style: const ButtonStyle(
                          visualDensity: VisualDensity.compact,
                        ),
                        showSelectedIcon: false,
                        segments: const [
                          ButtonSegment(value: false, label: Text('Inline')),
                          ButtonSegment(
                            value: true,
                            label: Text('Side by side'),
                          ),
                        ],
                        selected: {_split},
                        onSelectionChanged: (v) =>
                            setState(() => _split = v.first),
                      ),
                      IconButton(
                        tooltip: 'Previous change (⌥↑)',
                        onPressed: d == null || d.hunks.isEmpty
                            ? null
                            : () => _gotoHunk(_currentHunk - 1),
                        icon: const Icon(
                          Icons.keyboard_arrow_up_rounded,
                          size: 18,
                        ),
                      ),
                      IconButton(
                        tooltip: 'Next change (⌥↓)',
                        onPressed: d == null || d.hunks.isEmpty
                            ? null
                            : () => _gotoHunk(_currentHunk + 1),
                        icon: const Icon(
                          Icons.keyboard_arrow_down_rounded,
                          size: 18,
                        ),
                      ),
                      ..._fileButtons(),
                      IconButton(
                        tooltip: 'Close (Esc)',
                        onPressed: () => Navigator.pop(context),
                        icon: const Icon(Icons.close_rounded, size: 18),
                      ),
                    ],
                  ),
                ),
                const Divider(height: 1),
                if (notice != null && notice.error)
                  GitNoticeBanner(
                    text: notice.text,
                    error: true,
                    onDismiss: _git.dismissNotice,
                  ),
                Expanded(child: _body(d)),
              ],
            ),
          ),
        ),
      ),
    );
  }

  List<Widget> _fileButtons() {
    final paths = [widget.path];
    Widget btn(
      String label,
      IconData icon,
      Future<bool> Function() action, {
      bool danger = false,
    }) => GitButton(
      label: label,
      icon: icon,
      tone: danger ? GitButtonTone.danger : GitButtonTone.normal,
      onPressed: () => _fileAction(action),
    );
    return switch (widget.source) {
      DiffSource.working => [
        btn('Stage File', Icons.add_rounded, () => _git.stage(paths)),
        btn(
          'Discard File',
          Icons.undo_rounded,
          () => _confirmDiscard(paths),
          danger: true,
        ),
      ],
      DiffSource.untracked => [
        btn('Stage File', Icons.add_rounded, () => _git.stage(paths)),
        btn(
          'Delete File',
          Icons.delete_outline_rounded,
          () => _confirmDiscard(paths),
          danger: true,
        ),
      ],
      DiffSource.staged => [
        btn('Unstage File', Icons.remove_rounded, () => _git.unstage(paths)),
      ],
      DiffSource.commit => const [],
    };
  }

  Future<bool> _confirmDiscard(List<String> paths) async {
    final untracked = widget.source == DiffSource.untracked;
    final yes = await confirmDanger(
      context,
      title: untracked ? 'Delete untracked file?' : 'Discard changes?',
      message: '${paths.join(', ')}\n\nThis cannot be undone.',
      action: untracked ? 'Delete' : 'Discard',
    );
    return yes && await _git.discard(paths);
  }

  Widget _body(FileDiff? d) {
    if (_error != null) {
      return Center(
        child: SelectableText(
          _error!,
          style: const TextStyle(color: GC.downText),
        ),
      );
    }
    if (d == null) {
      return const Center(child: CircularProgressIndicator(strokeWidth: 2));
    }
    if (d.binary) {
      return const Center(child: Text('Binary file — no text diff'));
    }
    if (d.isEmpty) return const Center(child: Text('No changes'));
    final lineCount = d.hunks.fold<int>(0, (s, h) => s + h.lines.length);
    return SelectionArea(
      child: Scrollbar(
        controller: _scroll,
        child: SingleChildScrollView(
          controller: _scroll,
          padding: const EdgeInsets.only(bottom: 24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              if (lineCount > 20000)
                const Padding(
                  padding: EdgeInsets.all(8),
                  child: Text(
                    'Large diff — rendering may be slow',
                    style: TextStyle(color: GC.warn, fontSize: 12),
                  ),
                ),
              for (var i = 0; i < d.hunks.length; i++) ...[
                _hunkHeader(d.hunks[i], i),
                DiffHunkLines(hunk: d.hunks[i], split: _split),
              ],
            ],
          ),
        ),
      ),
    );
  }

  Widget _hunkHeader(DiffHunk h, int i) {
    final actions = <Widget>[
      if (widget.source == DiffSource.working) ...[
        _HunkButton(
          'Stage Hunk',
          Icons.add_rounded,
          () => _hunkAction(h, cached: true, reverse: false),
        ),
        _HunkButton('Discard Hunk', Icons.undo_rounded, () async {
          if (await confirmDanger(
            context,
            title: 'Discard this change?',
            message: 'This cannot be undone.',
            action: 'Discard',
          )) {
            await _hunkAction(h, cached: false, reverse: true);
          }
        }, danger: true),
      ],
      if (widget.source == DiffSource.staged)
        _HunkButton(
          'Unstage Hunk',
          Icons.remove_rounded,
          () => _hunkAction(h, cached: true, reverse: true),
        ),
    ];
    return Container(
      key: _hunkKeys[i],
      color: GitColors.hunkBg,
      padding: const EdgeInsets.fromLTRB(12, 4, 8, 4),
      margin: EdgeInsets.only(top: i == 0 ? 0 : 6),
      child: Row(
        children: [
          Expanded(
            child: Text(
              h.header,
              overflow: TextOverflow.ellipsis,
              style: GC.code.copyWith(fontSize: 12, color: GitColors.hunkFg),
            ),
          ),
          ...actions,
        ],
      ),
    );
  }
}

/// One hunk's lines, unified (old/new line numbers, +/− gutter) or side by
/// side (deletions paired with the additions that replace them).
class DiffHunkLines extends StatelessWidget {
  const DiffHunkLines({super.key, required this.hunk, this.split = false});

  final DiffHunk hunk;
  final bool split;

  static final _code = GC.code.copyWith(height: 1.45);

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: split ? [..._splitLines()] : [..._unifiedLines()],
  );

  Widget _num(int? n) => SizedBox(
    width: 46,
    child: Text(
      n?.toString() ?? '',
      textAlign: TextAlign.right,
      style: _code.copyWith(color: GC.textFaint, fontSize: 11.5),
    ),
  );

  Iterable<Widget> _unifiedLines() sync* {
    for (final l in hunk.lines) {
      if (l.kind == DiffLineKind.meta) {
        yield Padding(
          padding: const EdgeInsets.only(left: 108),
          child: Text(
            l.text,
            style: _code.copyWith(
              color: GC.textFaint,
              fontStyle: FontStyle.italic,
            ),
          ),
        );
        continue;
      }
      final (bg, fg, sign) = switch (l.kind) {
        DiffLineKind.add => (GitColors.addedBg, GitColors.addedFg, '+'),
        DiffLineKind.del => (GitColors.removedBg, GitColors.removedFg, '−'),
        _ => (Colors.transparent, GC.textBody, ' '),
      };
      yield Container(
        color: bg,
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _num(l.oldNo),
            _num(l.newNo),
            SizedBox(
              width: 18,
              child: Text(
                sign,
                textAlign: TextAlign.center,
                style: _code.copyWith(color: fg),
              ),
            ),
            Expanded(
              child: Text(
                l.text,
                style: _code.copyWith(
                  color: l.kind == DiffLineKind.context
                      ? GC.textBody
                      : GC.textHi,
                ),
              ),
            ),
          ],
        ),
      );
    }
  }

  Iterable<Widget> _splitLines() sync* {
    Widget side(DiffLine? l, bool left) {
      if (l == null) return Container(color: GitColors.emptySide);
      final changed = l.kind != DiffLineKind.context;
      final bg = changed
          ? (left ? GitColors.removedBg : GitColors.addedBg)
          : Colors.transparent;
      return Container(
        color: bg,
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _num(left ? l.oldNo : l.newNo),
            const SizedBox(width: 10),
            Expanded(
              child: Text(
                l.text,
                style: _code.copyWith(color: changed ? GC.textHi : GC.textBody),
              ),
            ),
          ],
        ),
      );
    }

    for (final r in splitRows(hunk)) {
      yield IntrinsicHeight(
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Expanded(child: side(r.left, true)),
            const VerticalDivider(width: 1),
            Expanded(child: side(r.right, false)),
          ],
        ),
      );
    }
  }
}

class _HunkButton extends StatelessWidget {
  const _HunkButton(
    this.label,
    this.icon,
    this.onPressed, {
    this.danger = false,
  });

  final String label;
  final IconData icon;
  final VoidCallback onPressed;
  final bool danger;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(left: 6),
    child: GitButton(
      dense: true,
      label: label,
      icon: icon,
      tone: danger ? GitButtonTone.danger : GitButtonTone.primary,
      onPressed: onPressed,
    ),
  );
}

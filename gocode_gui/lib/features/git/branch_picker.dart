import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/api/git_models.dart';
import 'git_state.dart';
import 'git_widgets.dart';

// Ported from goide (app/lib/git/branch_picker.dart).

/// Searchable branch switcher: local + remote branches, create, and
/// per-branch merge / rename / delete.
Future<void> showBranchPicker(BuildContext context) =>
    showDialog<void>(context: context, builder: (_) => const BranchPicker());

class BranchPicker extends ConsumerStatefulWidget {
  const BranchPicker({super.key});

  @override
  ConsumerState<BranchPicker> createState() => _BranchPickerState();
}

class _BranchPickerState extends ConsumerState<BranchPicker> {
  final _query = TextEditingController();
  GitBranches? _branches;
  String? _error;
  int _selected = 0;

  @override
  void initState() {
    super.initState();
    _query.addListener(() => setState(() => _selected = 0));
    _load();
  }

  @override
  void dispose() {
    _query.dispose();
    super.dispose();
  }

  GitNotifier get _git => ref.read(gitProvider.notifier);

  Future<void> _load() async {
    try {
      final b = await _git.branches();
      if (mounted) setState(() => _branches = b);
    } catch (e) {
      if (mounted) setState(() => _error = '$e');
    }
  }

  /// Rows in display order: (kind, value).
  List<(String, String)> get _rows {
    final typed = _query.text.trim();
    final q = typed.toLowerCase();
    final b = _branches;
    final rows = <(String, String)>[];
    if (q.isNotEmpty && !(b?.local.contains(typed) ?? false)) {
      rows.add(('create', typed));
    }
    if (b == null) return rows;
    for (final l in b.branches) {
      if (q.isEmpty || l.name.toLowerCase().contains(q)) {
        rows.add(('local', l.name));
      }
    }
    for (final r in b.remote) {
      if (q.isEmpty || r.toLowerCase().contains(q)) rows.add(('remote', r));
    }
    return rows;
  }

  Future<void> _activate((String, String) row) async {
    final nav = Navigator.of(context);
    final ok = switch (row.$1) {
      'create' => await _git.createBranch(row.$2.replaceAll(' ', '-')),
      _ => row.$2 == _branches?.current ? true : await _git.checkout(row.$2),
    };
    if (ok && mounted) nav.pop();
  }

  @override
  Widget build(BuildContext context) {
    final rows = _rows;
    final busy = ref.watch(gitProvider.select((g) => g.busy));
    final notice = ref.watch(gitProvider.select((g) => g.notice));
    final infos = {
      for (final b in _branches?.branches ?? const <GitBranchInfo>[]) b.name: b,
    };

    final children = <Widget>[];
    String? lastKind;
    for (var i = 0; i < rows.length; i++) {
      final (kind, value) = rows[i];
      if (kind != lastKind && kind != 'create') {
        children.add(
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 10, 14, 4),
            child: Text(
              kind == 'local' ? 'LOCAL BRANCHES' : 'REMOTE BRANCHES',
              style: GC.caption,
            ),
          ),
        );
      }
      lastKind = kind;
      children.add(
        _BranchRow(
          kind: kind,
          name: value,
          info: infos[value],
          current: value == _branches?.current,
          selected: i == _selected,
          onTap: () => _activate(rows[i]),
          onAction: (action) => _rowAction(action, value),
        ),
      );
    }

    int clampRow(int i) => i.clamp(0, rows.isEmpty ? 0 : rows.length - 1);

    return Dialog(
      alignment: Alignment.topCenter,
      insetPadding: const EdgeInsets.fromLTRB(16, 60, 16, 16),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        width: 620,
        height: 520,
        child: CallbackShortcuts(
          bindings: {
            const SingleActivator(LogicalKeyboardKey.arrowDown): () =>
                setState(() => _selected = clampRow(_selected + 1)),
            const SingleActivator(LogicalKeyboardKey.arrowUp): () =>
                setState(() => _selected = clampRow(_selected - 1)),
            const SingleActivator(LogicalKeyboardKey.enter): () {
              if (rows.isNotEmpty) _activate(rows[_selected]);
            },
          },
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Padding(
                padding: const EdgeInsets.all(10),
                child: TextField(
                  controller: _query,
                  autofocus: true,
                  decoration: const InputDecoration(
                    hintText: 'Switch to branch, or type a new branch name…',
                    prefixIcon: Icon(Icons.account_tree_outlined, size: 16),
                  ),
                ),
              ),
              if (busy != null) const LinearProgressIndicator(minHeight: 2),
              if (notice != null && notice.error)
                GitNoticeBanner(text: notice.text, error: true),
              if (_error != null)
                Padding(
                  padding: const EdgeInsets.all(12),
                  child: Text(
                    _error!,
                    style: const TextStyle(color: GC.downText),
                  ),
                ),
              Expanded(
                child: _branches == null && _error == null
                    ? const Center(
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : ListView(
                        padding: const EdgeInsets.only(bottom: 8),
                        children: children,
                      ),
              ),
              const Divider(height: 1),
              Padding(
                padding: const EdgeInsets.fromLTRB(12, 6, 12, 8),
                child: Row(
                  children: [
                    GitButton(
                      onPressed: _createFrom,
                      icon: Icons.call_split_rounded,
                      label: 'Create branch from…',
                    ),
                    const Spacer(),
                    const Text(
                      '↵ switch  ·  ↑↓ select',
                      style: TextStyle(fontSize: 12, color: GC.textDim),
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Future<void> _createFrom() async {
    final base = await promptText(
      context,
      title: 'Create branch from',
      hint: 'branch, tag or commit (e.g. origin/main)',
      action: 'Next',
    );
    if (base == null || base.isEmpty || !mounted) return;
    final name = await promptText(
      context,
      title: 'New branch name',
      hint: 'feature/…',
      action: 'Create',
    );
    if (name == null || name.isEmpty) return;
    if (await _git.createBranch(name, startPoint: base) && mounted) {
      Navigator.pop(context);
    }
  }

  Future<void> _rowAction(String action, String name) async {
    switch (action) {
      case 'merge':
        if (await confirmDanger(
          context,
          title: 'Merge "$name" into "${_branches?.current}"?',
          message: 'Conflicts, if any, are listed in Source Control.',
          action: 'Merge',
        )) {
          if (await _git.branchOp('merge', name) && mounted) {
            Navigator.pop(context);
          }
        }
      case 'rename':
        final newName = await promptText(
          context,
          title: 'Rename branch',
          initial: name,
          action: 'Rename',
        );
        if (newName != null && newName.isNotEmpty && newName != name) {
          await _git.branchOp('rename', name, newName: newName);
          await _load();
        }
      case 'delete':
        if (await confirmDanger(
          context,
          title: 'Delete branch "$name"?',
          message: 'Unmerged commits on it will be lost.',
          action: 'Delete',
        )) {
          await _git.branchOp('delete', name, force: true);
          await _load();
        }
    }
  }
}

class _BranchRow extends StatefulWidget {
  const _BranchRow({
    required this.kind,
    required this.name,
    required this.info,
    required this.current,
    required this.selected,
    required this.onTap,
    required this.onAction,
  });

  final String kind;
  final String name;
  final GitBranchInfo? info;
  final bool current;
  final bool selected;
  final VoidCallback onTap;
  final void Function(String action) onAction;

  @override
  State<_BranchRow> createState() => _BranchRowState();
}

class _BranchRowState extends State<_BranchRow> {
  bool _hover = false;

  @override
  Widget build(BuildContext context) {
    final info = widget.info;
    final create = widget.kind == 'create';
    final faint = const TextStyle(
      fontFamily: GC.sans,
      fontSize: 11,
      color: GC.textFaint,
    );
    return MouseRegion(
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() => _hover = false),
      child: InkWell(
        onTap: widget.onTap,
        child: Container(
          height: 38,
          padding: const EdgeInsets.symmetric(horizontal: 14),
          color: widget.selected
              ? GC.accent.withValues(alpha: 0.12)
              : (_hover ? GC.surface3 : null),
          child: Row(
            children: [
              Icon(
                create
                    ? Icons.add_rounded
                    : widget.current
                    ? Icons.check_rounded
                    : widget.kind == 'remote'
                    ? Icons.cloud_outlined
                    : Icons.account_tree_outlined,
                size: 15,
                color: create || widget.current ? GC.accent : GC.textDim,
              ),
              const SizedBox(width: 10),
              Flexible(
                child: Text(
                  create ? 'Create new branch "${widget.name}"' : widget.name,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    fontFamily: GC.sans,
                    fontSize: 13,
                    color: GC.textHi,
                    fontWeight: widget.current
                        ? FontWeight.w600
                        : FontWeight.normal,
                  ),
                ),
              ),
              if (info != null && info.upstream.isNotEmpty) ...[
                const SizedBox(width: 8),
                Text(
                  [
                    if (info.behind > 0) '↓${info.behind}',
                    if (info.ahead > 0) '↑${info.ahead}',
                    info.upstream,
                  ].join(' '),
                  style: faint,
                ),
              ],
              const Spacer(),
              if (info != null && !_hover)
                Text(relativeTime(info.lastCommitTime), style: faint),
              if (_hover && widget.kind == 'local' && !widget.current) ...[
                _act('Merge into current branch', Icons.merge_rounded, 'merge'),
                _act('Rename', Icons.edit_outlined, 'rename'),
                _act('Delete', Icons.delete_outline_rounded, 'delete'),
              ],
              if (_hover && widget.kind == 'local' && widget.current)
                _act('Rename', Icons.edit_outlined, 'rename'),
              if (_hover && widget.kind == 'remote')
                _act('Merge into current branch', Icons.merge_rounded, 'merge'),
            ],
          ),
        ),
      ),
    );
  }

  Widget _act(String tip, IconData icon, String action) => GitIconButton(
    tooltip: tip,
    icon: icon,
    color: action == 'delete' ? GC.downText : null,
    onPressed: () => widget.onAction(action),
  );
}

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:http/http.dart' as http;

import '../../app/theme.dart';
import '../../core/api/client.dart' show ApiException;
import '../../core/api/git_models.dart';
import 'branch_picker.dart';
import 'diff_view.dart';
import 'git_state.dart';
import 'git_widgets.dart';
import 'history_view.dart' show historyPathProvider;

// Ported from goide (app/lib/panels/git_panel.dart). Divergences: AI
// commit messages come from the server's model (POST
// /api/vcs/git/commit-message) rather than a separate ACP agent, and there
// is no "Open File" (gocode_gui has no editor).

/// The Source Control view: branch/sync toolbar, commit box, conflicts,
/// staged and unstaged changes, stashes.
class GitChangesView extends ConsumerStatefulWidget {
  const GitChangesView({super.key, required this.onShowHistory});

  /// Switches to the history view (after setting [historyPathProvider]).
  final VoidCallback onShowHistory;

  @override
  ConsumerState<GitChangesView> createState() => _GitChangesViewState();
}

class _GitChangesViewState extends ConsumerState<GitChangesView> {
  final _message = TextEditingController();
  final _collapsed = <String>{};
  bool _amend = false;
  bool _signoff = false;

  /// In-flight AI draft of the commit message (completing it stops the
  /// request), and how the last one ended.
  Completer<void>? _draft;
  String? _draftError;
  String? _draftModel;

  @override
  void dispose() {
    _stopGenerating();
    _message.dispose();
    super.dispose();
  }

  GitNotifier get _git => ref.read(gitProvider.notifier);

  static const _small = TextStyle(fontFamily: GC.sans, fontSize: 12);

  @override
  Widget build(BuildContext context) {
    final git = ref.watch(gitProvider);

    if (git.status == null) {
      if (git.loadError != null) {
        return ListView(
          padding: const EdgeInsets.all(16),
          children: [GitNoticeBanner(text: git.loadError!, error: true)],
        );
      }
      return const Center(
        child: SizedBox(
          width: 16,
          height: 16,
          child: CircularProgressIndicator(strokeWidth: 2),
        ),
      );
    }
    if (!git.isRepo) return _notARepo(git);

    final conflicts = git.conflicts;
    final staged = git.staged;
    final changes = git.changes;

    // Only the toolbar is fixed; banners, the commit box and the file lists
    // scroll together.
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _toolbar(git),
        if (git.busy != null)
          const LinearProgressIndicator(minHeight: 2)
        else
          const SizedBox(height: 2),
        Expanded(
          child: ListView(
            padding: const EdgeInsets.only(bottom: 12),
            children: [
              if (git.operation.isNotEmpty) _operationBanner(git),
              if (git.notice != null)
                GitNoticeBanner(
                  text: git.notice!.text,
                  error: git.notice!.error,
                  onDismiss: _git.dismissNotice,
                ),
              _commitBox(git),
              if (conflicts.isNotEmpty)
                ..._section('conflicts', 'Merge Conflicts', conflicts, [
                  _SectionAction(
                    'Stage resolved',
                    Icons.done_all_rounded,
                    () => _git.stage(conflicts.map((f) => f.path).toList()),
                  ),
                ]),
              if (staged.isNotEmpty)
                ..._section('staged', 'Staged Changes', staged, [
                  _SectionAction(
                    'Unstage all',
                    Icons.remove_rounded,
                    _git.unstageAll,
                  ),
                ]),
              ..._section('changes', 'Changes', changes, [
                if (changes.isNotEmpty) ...[
                  _SectionAction(
                    'Discard all',
                    Icons.undo_rounded,
                    _discardAll,
                    danger: true,
                  ),
                  _SectionAction('Stage all', Icons.add_rounded, _git.stageAll),
                ],
              ]),
              if (conflicts.isEmpty && staged.isEmpty && changes.isEmpty)
                const Padding(
                  padding: EdgeInsets.fromLTRB(16, 4, 16, 8),
                  child: Text(
                    'No changes — working tree clean',
                    style: TextStyle(
                      fontFamily: GC.sans,
                      fontSize: 12,
                      color: GC.textFaint,
                    ),
                  ),
                ),
              if (git.stashes.isNotEmpty) ..._stashSection(git.stashes),
            ],
          ),
        ),
      ],
    );
  }

  // --- not a repository -------------------------------------------------------

  Widget _notARepo(GitState git) => ListView(
    padding: const EdgeInsets.all(16),
    children: [
      const Text('This folder is not a git repository.'),
      const SizedBox(height: 12),
      Align(
        alignment: Alignment.centerLeft,
        child: FilledButton.icon(
          onPressed: git.busy != null ? null : _git.init,
          icon: const Icon(Icons.add_rounded, size: 16),
          label: const Text('Initialize Repository'),
        ),
      ),
      if (git.notice != null) ...[
        const SizedBox(height: 10),
        GitNoticeBanner(
          text: git.notice!.text,
          error: git.notice!.error,
          onDismiss: _git.dismissNotice,
        ),
      ],
    ],
  );

  // --- toolbar --------------------------------------------------------------------

  Widget _toolbar(GitState git) {
    final st = git.status!;
    final syncLabel = [
      if (git.behind > 0) '${git.behind}↓',
      if (git.ahead > 0) '${git.ahead}↑',
    ].join(' ');
    return Padding(
      padding: const EdgeInsets.fromLTRB(6, 6, 4, 2),
      child: Row(
        children: [
          // The branch name gets all the room the buttons leave.
          Expanded(
            child: Align(
              alignment: Alignment.centerLeft,
              child: Tooltip(
                message: st.detached
                    ? 'Detached HEAD at ${st.head}'
                    : 'Switch branch',
                child: InkWell(
                  borderRadius: BorderRadius.circular(6),
                  onTap: () => showBranchPicker(context),
                  child: Padding(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 6,
                      vertical: 5,
                    ),
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        Icon(
                          st.detached
                              ? Icons.commit_rounded
                              : Icons.account_tree_outlined,
                          size: 14,
                          color: GC.accent,
                        ),
                        const SizedBox(width: 6),
                        Flexible(
                          child: Text(
                            st.detached ? '(${st.head})' : git.branch,
                            overflow: TextOverflow.ellipsis,
                            style: const TextStyle(
                              fontFamily: GC.sans,
                              fontSize: 13,
                              fontWeight: FontWeight.w600,
                              color: GC.textHi,
                            ),
                          ),
                        ),
                        const Icon(
                          Icons.expand_more_rounded,
                          size: 15,
                          color: GC.textDim,
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            ),
          ),
          if (git.hasRemote || git.hasUpstream) ...[
            GitButton(
              tooltip: !git.hasUpstream
                  ? 'Publish branch to remote'
                  : 'Sync: pull ${git.behind}, push ${git.ahead} (${st.upstream})',
              tone: GitButtonTone.primary,
              onPressed: git.busy != null ? null : _git.sync,
              icon: !git.hasUpstream
                  ? Icons.cloud_upload_outlined
                  : Icons.sync_rounded,
              label: !git.hasUpstream
                  ? 'Publish'
                  : (syncLabel.isEmpty ? 'Sync' : 'Sync $syncLabel'),
            ),
            const SizedBox(width: 6),
          ],
          if (git.hasRemote) ...[
            GitButton(
              tooltip: 'Fetch from all remotes',
              onPressed: git.busy != null ? null : _git.fetch,
              icon: Icons.cloud_download_outlined,
              label: 'Fetch',
            ),
            const SizedBox(width: 6),
          ],
          GitButton(
            tooltip: 'Show commit history',
            onPressed: () {
              ref.read(historyPathProvider.notifier).set('');
              widget.onShowHistory();
            },
            icon: Icons.history_rounded,
            label: 'History',
          ),
          const SizedBox(width: 2),
          _moreMenu(git),
        ],
      ),
    );
  }

  Widget _moreMenu(GitState git) {
    MenuItemButton item(String label, IconData icon, VoidCallback? onPressed) =>
        MenuItemButton(
          leadingIcon: Icon(icon, size: 15),
          onPressed: git.busy != null ? null : onPressed,
          child: Text(label),
        );
    return MenuAnchor(
      menuChildren: [
        if (git.hasRemote) ...[
          item('Pull', Icons.download_rounded, () => _git.pull()),
          item(
            'Pull (Rebase)',
            Icons.download_rounded,
            () => _git.pull(rebase: true),
          ),
          item(
            git.hasUpstream ? 'Push' : 'Publish Branch',
            Icons.upload_rounded,
            () => _git.push(),
          ),
          item(
            'Force Push (with lease)…',
            Icons.warning_amber_rounded,
            () async {
              if (await confirmDanger(
                context,
                title: 'Force push ${git.branch}?',
                message: 'Overwrites the remote branch if nobody else pushed since your last fetch.',
                action: 'Force Push',
              )) {
                await _git.push(force: true);
              }
            },
          ),
          item('Push Tags', Icons.sell_outlined, _git.pushTags),
          item(
            'Fetch (all remotes, prune)',
            Icons.cloud_download_outlined,
            _git.fetch,
          ),
          const Divider(height: 8),
        ],
        item(
          'Stash All Changes',
          Icons.inventory_2_outlined,
          () => _git.stash('push'),
        ),
        item('Stash All Changes…', Icons.inventory_2_outlined, () async {
          final msg = await promptText(
            context,
            title: 'Stash message',
            action: 'Stash',
          );
          if (msg != null) await _git.stash('push', message: msg);
        }),
        if (git.stashes.isNotEmpty)
          item(
            'Pop Latest Stash',
            Icons.unarchive_outlined,
            () => _git.stash('pop'),
          ),
        const Divider(height: 8),
        item(
          'Switch Branch…',
          Icons.account_tree_outlined,
          () => showBranchPicker(context),
        ),
        item('Create Branch…', Icons.call_split_rounded, () async {
          final name = await promptText(
            context,
            title: 'New branch',
            hint: 'feature/…',
            action: 'Create',
          );
          if (name != null && name.isNotEmpty) await _git.createBranch(name);
        }),
        item('Refresh', Icons.refresh_rounded, _git.refresh),
      ],
      builder: (context, controller, _) => GitIconButton(
        tooltip: 'More actions',
        size: 34,
        onPressed: () =>
            controller.isOpen ? controller.close() : controller.open(),
        icon: Icons.more_horiz_rounded,
      ),
    );
  }

  // --- banners ----------------------------------------------------------------------

  Widget _operationBanner(GitState git) {
    final op = git.operation;
    final opName = '${op[0].toUpperCase()}${op.substring(1)}';
    final n = git.conflicts.length;
    return Container(
      margin: const EdgeInsets.fromLTRB(8, 4, 8, 4),
      padding: const EdgeInsets.fromLTRB(10, 6, 6, 6),
      decoration: BoxDecoration(
        color: GC.warn.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(GC.rInput),
        border: Border.all(color: GC.warn.withValues(alpha: 0.5)),
      ),
      child: Row(
        children: [
          const Icon(Icons.merge_rounded, size: 16, color: GC.warn),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              n > 0
                  ? '$opName in progress — resolve $n conflict${n == 1 ? '' : 's'}'
                  : '$opName in progress — conflicts resolved',
              style: _small.copyWith(color: GC.textHi),
            ),
          ),
          GitButton(
            dense: true,
            tone: GitButtonTone.danger,
            icon: Icons.close_rounded,
            label: 'Abort',
            onPressed: git.busy != null ? null : () => _git.conflict('abort'),
          ),
          const SizedBox(width: 6),
          FilledButton(
            style: FilledButton.styleFrom(visualDensity: VisualDensity.compact),
            onPressed: git.busy != null || n > 0
                ? null
                : () => _git.conflict('continue'),
            child: const Text('Continue'),
          ),
        ],
      ),
    );
  }

  // --- commit box ---------------------------------------------------------------------

  Future<void> _commit({bool all = false, bool push = false}) async {
    final git = ref.read(gitProvider);
    if (_message.text.trim().isEmpty && !_amend) return;
    // Like VS Code: with nothing staged, "Commit" commits all changes.
    final commitAll = all || (git.staged.isEmpty && !_amend);
    final ok = await _git.commit(
      _message.text,
      amend: _amend,
      all: commitAll,
      signoff: _signoff,
      push: push,
    );
    if (ok && mounted) {
      _message.clear();
      setState(() {
        _amend = false;
        _draftModel = null;
        _draftError = null;
      });
    }
  }

  /// Streams a model-written message into the box, replacing its text. It
  /// describes what Commit would take: the staged changes, or every change
  /// when nothing is staged.
  Future<void> _generateMessage(GitState git) async {
    if (_draft != null) return;
    final before = _message.text;
    final abort = Completer<void>();
    setState(() {
      _draft = abort;
      _draftError = null;
      _draftModel = null;
    });
    var got = false;
    try {
      final stream = _git.draftCommitMessage(
        stagedOnly: git.staged.isNotEmpty,
        abort: abort.future,
      );
      await for (final ev in stream) {
        if (!mounted) return;
        if (ev.error.isNotEmpty) {
          setState(() => _draftError = ev.error);
          break;
        }
        if (ev.model.isNotEmpty) _draftModel = ev.model;
        if (ev.text.isNotEmpty) {
          got = true;
          _message.value = TextEditingValue(
            text: ev.text,
            selection: TextSelection.collapsed(offset: ev.text.length),
          );
        }
      }
    } on http.RequestAbortedException {
      // Stopped by the user: keep whatever was written so far.
    } catch (e) {
      if (mounted) {
        setState(() => _draftError = e is ApiException ? e.message : '$e');
      }
    } finally {
      if (mounted) {
        if (!got) _message.text = before;
        setState(() => _draft = null);
      }
    }
  }

  void _stopGenerating() {
    final draft = _draft;
    if (draft != null && !draft.isCompleted) draft.complete();
  }

  Widget _commitBox(GitState git) {
    final generating = _draft != null;
    final busy = git.busy != null || generating;
    final nothingToDescribe = git.staged.isEmpty && git.changes.isEmpty;
    final nothing = nothingToDescribe && !_amend;
    final label = _amend
        ? 'Amend'
        : git.staged.isEmpty && git.changes.isNotEmpty
        ? 'Commit All'
        : 'Commit';
    return Padding(
      padding: const EdgeInsets.fromLTRB(8, 4, 8, 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          CallbackShortcuts(
            bindings: {
              const SingleActivator(LogicalKeyboardKey.enter, meta: true): () =>
                  _commit(),
              const SingleActivator(
                LogicalKeyboardKey.enter,
                control: true,
              ): () =>
                  _commit(),
            },
            child: Stack(
              children: [
                TextField(
                  controller: _message,
                  minLines: 2,
                  maxLines: 8,
                  readOnly: generating,
                  style: const TextStyle(fontSize: 13),
                  decoration: InputDecoration(
                    hintText: generating
                        ? 'Writing a commit message…'
                        : 'Message (⌘↵ to commit on "${git.branch}")',
                    // Room for the AI button in the top-right corner.
                    contentPadding: const EdgeInsets.fromLTRB(12, 10, 36, 10),
                  ),
                ),
                Positioned(
                  top: 2,
                  right: 2,
                  child: generating
                      ? IconButton(
                          tooltip: 'Stop generating',
                          onPressed: _stopGenerating,
                          icon: const Stack(
                            alignment: Alignment.center,
                            children: [
                              SizedBox.square(
                                dimension: 18,
                                child: CircularProgressIndicator(
                                  strokeWidth: 1.5,
                                ),
                              ),
                              Icon(Icons.stop_rounded, size: 11),
                            ],
                          ),
                        )
                      : IconButton(
                          tooltip: git.staged.isNotEmpty
                              ? 'Generate commit message with AI (staged changes)'
                              : 'Generate commit message with AI (all changes)',
                          color: GC.accentText,
                          onPressed: busy || nothingToDescribe
                              ? null
                              : () => _generateMessage(git),
                          icon: const Icon(
                            Icons.auto_awesome_rounded,
                            size: 17,
                          ),
                        ),
                ),
              ],
            ),
          ),
          if (_draftError != null)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                _draftError!,
                style: _small.copyWith(color: GC.downText),
              ),
            )
          else if (_draftModel != null &&
              !generating &&
              _message.text.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                'Written by $_draftModel — review before committing',
                style: _small.copyWith(color: GC.textDim),
              ),
            ),
          const SizedBox(height: 6),
          Row(
            children: [
              Expanded(
                child: FilledButton.icon(
                  onPressed: busy || nothing ? null : () => _commit(),
                  icon: Icon(
                    _amend ? Icons.edit_note_rounded : Icons.check_rounded,
                    size: 16,
                  ),
                  label: Text(label),
                ),
              ),
              const SizedBox(width: 4),
              MenuAnchor(
                menuChildren: [
                  MenuItemButton(
                    leadingIcon: const Icon(Icons.upload_rounded, size: 15),
                    onPressed: busy || nothing
                        ? null
                        : () => _commit(push: true),
                    child: const Text('Commit & Push'),
                  ),
                  MenuItemButton(
                    leadingIcon: const Icon(Icons.done_all_rounded, size: 15),
                    onPressed: busy || nothing
                        ? null
                        : () => _commit(all: true),
                    child: const Text('Commit All (incl. untracked)'),
                  ),
                  const Divider(height: 8),
                  CheckboxMenuButton(
                    value: _amend,
                    onChanged: (v) {
                      setState(() => _amend = v ?? false);
                      // Prefill with the last message when amending an empty box.
                      if (_amend && _message.text.trim().isEmpty) {
                        _message.text = git.status?.lastCommitMessage ?? '';
                      }
                    },
                    child: const Text('Amend Last Commit'),
                  ),
                  CheckboxMenuButton(
                    value: _signoff,
                    onChanged: (v) => setState(() => _signoff = v ?? false),
                    child: const Text('Sign Off'),
                  ),
                ],
                builder: (context, controller, _) => IconButton.filledTonal(
                  style: IconButton.styleFrom(
                    backgroundColor: GC.accent.withValues(alpha: 0.18),
                    foregroundColor: GC.accentText,
                  ),
                  tooltip: 'Commit options',
                  onPressed: () => controller.isOpen
                      ? controller.close()
                      : controller.open(),
                  icon: const Icon(Icons.expand_more_rounded, size: 18),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  // --- file sections --------------------------------------------------------------------

  Future<bool> _discardAll() async {
    final n = ref.read(gitProvider).changes.length;
    if (!await confirmDanger(
      context,
      title: 'Discard all changes?',
      message:
          '$n file${n == 1 ? '' : 's'}, including untracked files, will be reverted or deleted. '
          'This cannot be undone.',
      action: 'Discard All',
    )) {
      return false;
    }
    return _git.discardAll();
  }

  List<Widget> _section(
    String id,
    String title,
    List<GitFileStatus> files,
    List<_SectionAction> actions,
  ) {
    final open = !_collapsed.contains(id);
    return [
      _SectionHeader(
        title: title,
        count: files.length,
        open: open,
        actions: actions,
        onToggle: () =>
            setState(() => open ? _collapsed.add(id) : _collapsed.remove(id)),
      ),
      if (open)
        for (final f in files)
          _FileRow(
            file: f,
            kind: id,
            onOpenDiff: () => showGitDiff(
              context,
              path: f.path,
              source: id == 'staged'
                  ? DiffSource.staged
                  : f.status == '?'
                  ? DiffSource.untracked
                  : DiffSource.working,
            ),
            onAction: (a) => _fileAction(a, f),
          ),
    ];
  }

  Future<void> _fileAction(String action, GitFileStatus f) async {
    switch (action) {
      case 'stage':
        await _git.stage([f.path]);
      case 'unstage':
        await _git.unstage([f.path]);
      case 'discard':
        final untracked = f.status == '?';
        if (await confirmDanger(
          context,
          title: untracked
              ? 'Delete "${f.path}"?'
              : 'Discard changes in "${f.path}"?',
          message: 'This cannot be undone.',
          action: untracked ? 'Delete' : 'Discard',
        )) {
          await _git.discard([f.path]);
        }
      case 'ours' || 'theirs' || 'resolved':
        await _git.conflict(action, path: f.path);
      case 'history':
        ref.read(historyPathProvider.notifier).set(f.path);
        widget.onShowHistory();
      case 'copy':
        await Clipboard.setData(ClipboardData(text: f.path));
    }
  }

  List<Widget> _stashSection(List<GitStashEntry> stashes) {
    final open = !_collapsed.contains('stashes');
    return [
      _SectionHeader(
        title: 'Stashes',
        count: stashes.length,
        open: open,
        actions: const [],
        onToggle: () => setState(
          () => open ? _collapsed.add('stashes') : _collapsed.remove('stashes'),
        ),
      ),
      if (open)
        for (final s in stashes)
          _StashRow(
            entry: s,
            onAction: (op) async {
              if (op == 'drop' &&
                  !await confirmDanger(
                    context,
                    title: 'Drop stash?',
                    message: s.message,
                    action: 'Drop',
                  )) {
                return;
              }
              await _git.stash(op, index: s.index);
            },
          ),
    ];
  }
}

class _SectionAction {
  const _SectionAction(
    this.tooltip,
    this.icon,
    this.onPressed, {
    this.danger = false,
  });

  final String tooltip;
  final IconData icon;
  final Future<bool> Function() onPressed;
  final bool danger;
}

class _SectionHeader extends StatefulWidget {
  const _SectionHeader({
    required this.title,
    required this.count,
    required this.open,
    required this.actions,
    required this.onToggle,
  });

  final String title;
  final int count;
  final bool open;
  final List<_SectionAction> actions;
  final VoidCallback onToggle;

  @override
  State<_SectionHeader> createState() => _SectionHeaderState();
}

class _SectionHeaderState extends State<_SectionHeader> {
  @override
  Widget build(BuildContext context) => InkWell(
    onTap: widget.onToggle,
    child: Padding(
      padding: const EdgeInsets.fromLTRB(4, 6, 10, 4),
      child: Row(
        children: [
          Icon(
            widget.open
                ? Icons.expand_more_rounded
                : Icons.chevron_right_rounded,
            size: 18,
            color: GC.textBody,
          ),
          const SizedBox(width: 2),
          Text(
            widget.title.toUpperCase(),
            style: GC.caption.copyWith(fontSize: 12, color: GC.textBody),
          ),
          const SizedBox(width: 8),
          GitChip('${widget.count}'),
          const Spacer(),
          // Always shown, labeled: bulk actions are the panel's main verbs.
          for (final a in widget.actions) ...[
            const SizedBox(width: 6),
            GitButton(
              dense: true,
              label: a.tooltip,
              icon: a.icon,
              tone: a.danger ? GitButtonTone.danger : GitButtonTone.normal,
              onPressed: () => a.onPressed(),
            ),
          ],
        ],
      ),
    ),
  );
}

class _FileRow extends StatefulWidget {
  const _FileRow({
    required this.file,
    required this.kind,
    required this.onOpenDiff,
    required this.onAction,
  });

  final GitFileStatus file;
  final String kind; // conflicts | staged | changes
  final VoidCallback onOpenDiff;
  final void Function(String action) onAction;

  @override
  State<_FileRow> createState() => _FileRowState();
}

class _FileRowState extends State<_FileRow> {
  bool _hover = false;

  List<(String, IconData, String)> get _actions => switch (widget.kind) {
    'conflicts' => [
      ('Accept Current (ours)', Icons.keyboard_arrow_left_rounded, 'ours'),
      (
        'Accept Incoming (theirs)',
        Icons.keyboard_arrow_right_rounded,
        'theirs',
      ),
      ('Mark Resolved', Icons.check_rounded, 'resolved'),
    ],
    'staged' => [('Unstage', Icons.remove_rounded, 'unstage')],
    _ => [
      (
        widget.file.status == '?' ? 'Delete File' : 'Discard Changes',
        Icons.undo_rounded,
        'discard',
      ),
      ('Stage', Icons.add_rounded, 'stage'),
    ],
  };

  Future<void> _menu(Offset pos) async {
    final overlay = Overlay.of(context).context.findRenderObject() as RenderBox;
    PopupMenuItem<String> item(String v, String label) =>
        PopupMenuItem(value: v, height: 32, child: Text(label));
    final choice = await showMenu<String>(
      context: context,
      position: RelativeRect.fromRect(
        pos & const Size(1, 1),
        Offset.zero & overlay.size,
      ),
      items: [
        item('_diff', 'Open Changes'),
        const PopupMenuDivider(height: 8),
        for (final (label, _, action) in _actions) item(action, label),
        const PopupMenuDivider(height: 8),
        if (widget.file.status != '?') item('history', 'File History'),
        item('copy', 'Copy Path'),
      ],
    );
    switch (choice) {
      case null:
        return;
      case '_diff':
        widget.onOpenDiff();
      default:
        widget.onAction(choice);
    }
  }

  @override
  Widget build(BuildContext context) {
    final f = widget.file;
    final deleted = f.status == 'D';
    return MouseRegion(
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() => _hover = false),
      child: GestureDetector(
        onSecondaryTapDown: (d) => _menu(d.globalPosition),
        onLongPressStart: (d) => _menu(d.globalPosition),
        child: InkWell(
          onTap: widget.onOpenDiff,
          child: Container(
            height: 34,
            padding: const EdgeInsets.only(left: 26, right: 8),
            color: _hover ? GC.surface3 : null,
            child: Row(
              children: [
                const Icon(
                  Icons.description_outlined,
                  size: 14,
                  color: GC.textDim,
                ),
                const SizedBox(width: 6),
                // One text run: the name keeps its width, the folder takes
                // what's left and ellipsizes first.
                Expanded(
                  child: Text.rich(
                    TextSpan(
                      children: [
                        TextSpan(
                          text: baseName(f.path),
                          style: TextStyle(
                            fontSize: 13,
                            decoration: deleted
                                ? TextDecoration.lineThrough
                                : null,
                            color: widget.kind == 'conflicts'
                                ? GC.warn
                                : GitColors.forStatus(f.status),
                          ),
                        ),
                        TextSpan(
                          text:
                              '   ${f.origPath.isNotEmpty ? '← ${f.origPath}' : dirName(f.path)}',
                          style: const TextStyle(
                            fontSize: 12,
                            color: GC.textDim,
                          ),
                        ),
                      ],
                    ),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: const TextStyle(fontFamily: GC.sans),
                  ),
                ),
                if (_hover)
                  for (final (label, icon, action) in _actions)
                    GitIconButton(
                      tooltip: label,
                      icon: icon,
                      color: action == 'discard' ? GC.downText : null,
                      onPressed: () => widget.onAction(action),
                    ),
                const SizedBox(width: 4),
                StatusLetter(f.status),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

class _StashRow extends StatefulWidget {
  const _StashRow({required this.entry, required this.onAction});

  final GitStashEntry entry;
  final void Function(String op) onAction;

  @override
  State<_StashRow> createState() => _StashRowState();
}

class _StashRowState extends State<_StashRow> {
  bool _hover = false;

  @override
  Widget build(BuildContext context) => MouseRegion(
    onEnter: (_) => setState(() => _hover = true),
    onExit: (_) => setState(() => _hover = false),
    child: Container(
      height: 34,
      padding: const EdgeInsets.only(left: 26, right: 8),
      color: _hover ? GC.surface3 : null,
      child: Row(
        children: [
          const Icon(Icons.inventory_2_outlined, size: 14, color: GC.textDim),
          const SizedBox(width: 6),
          Expanded(
            child: Text(
              widget.entry.message,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(
                fontFamily: GC.sans,
                fontSize: 13,
                color: GC.textHi,
              ),
            ),
          ),
          if (_hover) ...[
            _btn('Apply', Icons.content_paste_go_rounded, 'apply'),
            _btn('Pop', Icons.unarchive_outlined, 'pop'),
            _btn('Drop', Icons.delete_outline_rounded, 'drop'),
          ] else
            Text(
              relativeTime(widget.entry.time),
              style: const TextStyle(fontSize: 12, color: GC.textDim),
            ),
        ],
      ),
    ),
  );

  Widget _btn(String label, IconData icon, String op) => Padding(
    padding: const EdgeInsets.only(left: 6),
    child: GitButton(
      dense: true,
      label: label,
      icon: icon,
      tone: op == 'drop' ? GitButtonTone.danger : GitButtonTone.normal,
      onPressed: () => widget.onAction(op),
    ),
  );
}

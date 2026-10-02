import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/client.dart';
import '../../core/api/git_models.dart';
import '../../core/connection/controller.dart';

// Ported from goide (app/lib/git/git_state.dart): the same operations over
// gocode's /api/vcs/git routes instead of the goide host's gRPC service.

/// The folder Source Control acts on; null means the server's own working
/// directory. Set by the Source Control screen from its route or the
/// selected project.
class GitDirectoryNotifier extends Notifier<String?> {
  @override
  String? build() => null;

  void set(String? directory) {
    final next = directory == null || directory.isEmpty ? null : directory;
    if (next != state) state = next;
  }
}

final gitDirectoryProvider = NotifierProvider<GitDirectoryNotifier, String?>(
  GitDirectoryNotifier.new,
);

/// Outcome of the last git operation, shown as a banner in the panel.
class GitNotice {
  const GitNotice(this.text, {this.error = false});

  final String text;
  final bool error;
}

class GitState {
  const GitState({
    this.status,
    this.stashes = const [],
    this.loading = false,
    this.busy,
    this.notice,
    this.loadError,
  });

  /// Latest status from the server (null until first load).
  final GitStatus? status;
  final List<GitStashEntry> stashes;
  final bool loading;

  /// Label of the running operation ("Pushing…"), null when idle.
  final String? busy;
  final GitNotice? notice;

  /// Why the last status read failed (no connection, server error).
  final String? loadError;

  bool get isRepo => status?.isRepo ?? false;
  String get branch => status?.branch ?? '';
  String get root => status?.root ?? '';
  List<GitFileStatus> get files => status?.files ?? const [];

  List<GitFileStatus> get conflicts =>
      files.where((f) => f.status == 'U').toList();
  List<GitFileStatus> get staged =>
      files.where((f) => f.staged && f.status != 'U').toList();
  List<GitFileStatus> get changes =>
      files.where((f) => !f.staged && f.status != 'U').toList();

  int get ahead => status?.ahead ?? 0;
  int get behind => status?.behind ?? 0;
  bool get hasUpstream => (status?.upstream ?? '').isNotEmpty;
  bool get hasRemote => (status?.remotes ?? const []).isNotEmpty;
  String get operation => status?.operation ?? '';

  GitState copyWith({
    GitStatus? status,
    List<GitStashEntry>? stashes,
    bool? loading,
    String? busy,
    bool clearBusy = false,
    GitNotice? notice,
    bool clearNotice = false,
    String? loadError,
    bool clearLoadError = false,
  }) => GitState(
    status: status ?? this.status,
    stashes: stashes ?? this.stashes,
    loading: loading ?? this.loading,
    busy: clearBusy ? null : (busy ?? this.busy),
    notice: clearNotice ? null : (notice ?? this.notice),
    loadError: clearLoadError ? null : (loadError ?? this.loadError),
  );
}

class GitNotifier extends Notifier<GitState> {
  Timer? _poll;
  bool _refreshing = false;

  @override
  GitState build() {
    // A different folder or connection starts over.
    ref.watch(gitDirectoryProvider);
    ref.watch(apiClientProvider);
    // Poll while someone watches: edits the agent makes, saves in an
    // editor, the user's own git in a terminal.
    _poll = Timer.periodic(const Duration(seconds: 5), (_) {
      if (state.busy == null) refresh();
    });
    ref.onDispose(() => _poll?.cancel());
    Future.microtask(refresh);
    return const GitState(loading: true);
  }

  String? get directory => ref.read(gitDirectoryProvider);

  GocodeClient get _client {
    final c = ref.read(apiClientProvider);
    if (c == null) throw StateError('not connected to a gocode server');
    return c;
  }

  Future<void> refresh() async {
    if (_refreshing) return;
    _refreshing = true;
    final dir = directory;
    try {
      final client = _client;
      final st = await client.gitStatus(directory: dir);
      if (!ref.mounted || dir != directory) return;
      var stashes = state.stashes;
      if (st.isRepo && st.stashCount != stashes.length) {
        stashes = await client.gitStashList(directory: dir);
        if (!ref.mounted || dir != directory) return;
      } else if (!st.isRepo) {
        stashes = const [];
      }
      state = state.copyWith(
        status: st,
        stashes: stashes,
        loading: false,
        clearLoadError: true,
      );
    } catch (e) {
      if (ref.mounted) {
        state = state.copyWith(loading: false, loadError: _message(e));
      }
    } finally {
      _refreshing = false;
    }
  }

  void dismissNotice() => state = state.copyWith(clearNotice: true);

  /// Runs one operation: shows [label] while busy, reports the error (or
  /// [success] text) as a notice, then refreshes. Returns true on success.
  Future<bool> run(
    String label,
    Future<void> Function(GocodeClient c, String? dir) op, {
    String? success,
  }) async {
    if (state.busy != null) return false;
    state = state.copyWith(busy: label, clearNotice: true);
    var ok = true;
    try {
      await op(_client, directory);
      if (success != null) state = state.copyWith(notice: GitNotice(success));
    } catch (e) {
      ok = false;
      state = state.copyWith(notice: GitNotice(_message(e), error: true));
    } finally {
      if (ref.mounted) state = state.copyWith(clearBusy: true);
    }
    if (ref.mounted) await refresh();
    return ok;
  }

  Future<bool> _op(
    String label,
    String op,
    Map<String, Object?> body, {
    String? success,
  }) => run(
    label,
    (c, dir) => c.gitOp(op, body, directory: dir),
    success: success,
  );

  static String _message(Object e) {
    final text = e is ApiException ? e.message : '$e';
    return text
        .replaceAll(RegExp(r'^(error|fatal): ', multiLine: true), '')
        .trim();
  }

  // --- working tree ---------------------------------------------------------

  Future<bool> stage(List<String> paths) =>
      _op('Staging…', 'stage', {'paths': paths});

  Future<bool> stageAll() => _op('Staging…', 'stage', {'all': true});

  Future<bool> unstage(List<String> paths) =>
      _op('Unstaging…', 'stage', {'paths': paths, 'unstage': true});

  Future<bool> unstageAll() =>
      _op('Unstaging…', 'stage', {'all': true, 'unstage': true});

  Future<bool> discard(List<String> paths) =>
      _op('Discarding…', 'discard', {'paths': paths});

  Future<bool> discardAll() =>
      _op('Discarding…', 'discard', {'all': true, 'includeUntracked': true});

  /// Applies a single-hunk patch: stage (cached), unstage (cached+reverse)
  /// or discard (reverse).
  Future<bool> applyPatch(
    String patch, {
    required bool cached,
    required bool reverse,
  }) => _op(
    cached
        ? (reverse ? 'Unstaging hunk…' : 'Staging hunk…')
        : 'Discarding hunk…',
    'apply',
    {'patch': patch, 'cached': cached, 'reverse': reverse},
  );

  Future<String> diff(
    String path, {
    bool staged = false,
    bool untracked = false,
    String commit = '',
  }) => _client.gitDiff(
    path,
    directory: directory,
    staged: staged,
    untracked: untracked,
    commit: commit,
  );

  // --- commits ----------------------------------------------------------------

  /// Asks the model to draft a commit message for the staged changes
  /// ([stagedOnly]) or for everything "Commit All" would take. Events carry
  /// the message so far; completing [abort] stops the draft.
  Stream<CommitMessageDraft> draftCommitMessage({
    required bool stagedOnly,
    Future<void>? abort,
  }) => _client.gitCommitMessage(
    directory: directory,
    stagedOnly: stagedOnly,
    abort: abort,
  );

  Future<bool> commit(
    String message, {
    bool amend = false,
    bool all = false,
    bool signoff = false,
    bool push = false,
  }) async {
    final ok = await _op(amend ? 'Amending…' : 'Committing…', 'commit', {
      'message': message,
      'amend': amend,
      'all': all,
      'signoff': signoff,
    });
    if (ok && push) return this.push();
    return ok;
  }

  Future<bool> init() => _op(
    'Initializing…',
    'init',
    const {},
    success: 'Initialized a new repository',
  );

  // --- remotes ------------------------------------------------------------------

  Future<bool> _remote(
    String label,
    Map<String, Object?> body,
    String success,
  ) => _op(label, 'remote', body, success: success);

  Future<bool> fetch() =>
      _remote('Fetching…', {'op': 'fetch', 'all': true}, 'Fetched');

  Future<bool> pull({bool rebase = false}) => _remote(
    rebase ? 'Pulling (rebase)…' : 'Pulling…',
    {'op': 'pull', 'rebase': rebase},
    'Pulled',
  );

  Future<bool> push({bool force = false}) {
    if (!state.hasUpstream) return publish();
    return _remote(force ? 'Force pushing…' : 'Pushing…', {
      'op': 'push',
      'force': force,
    }, 'Pushed');
  }

  Future<bool> publish() => _remote('Publishing branch…', {
    'op': 'push',
    'setUpstream': true,
  }, 'Published ${state.branch}');

  Future<bool> pushTags() =>
      _remote('Pushing tags…', {'op': 'push', 'tags': true}, 'Pushed tags');

  /// Pull then push (or publish a branch without upstream).
  Future<bool> sync() async {
    if (!state.hasUpstream) return publish();
    if (state.behind > 0 && !await pull()) return false;
    if (state.ahead > 0) return push();
    return true;
  }

  // --- branches -------------------------------------------------------------------

  Future<GitBranches> branches() => _client.gitBranches(directory: directory);

  Future<bool> checkout(String target) =>
      _op('Checking out $target…', 'checkout', {'branch': target});

  Future<bool> createBranch(String name, {String startPoint = ''}) => _op(
    'Creating $name…',
    'checkout',
    {'branch': name, 'create': true, 'startPoint': startPoint},
    success: 'Switched to new branch $name',
  );

  Future<bool> branchOp(
    String op,
    String name, {
    String newName = '',
    bool force = false,
  }) => _op(
    switch (op) {
      'merge' => 'Merging $name…',
      'rename' => 'Renaming…',
      _ => 'Deleting $name…',
    },
    'branch',
    {'op': op, 'name': name, 'newName': newName, 'force': force},
    success: switch (op) {
      'merge' => 'Merged $name',
      'rename' => 'Renamed to $newName',
      _ => 'Deleted $name',
    },
  );

  // --- history ------------------------------------------------------------------------

  Future<List<GitCommitInfo>> log({
    int limit = 200,
    int skip = 0,
    bool all = false,
    String query = '',
    String path = '',
  }) => _client.gitLog(
    directory: directory,
    limit: limit,
    skip: skip,
    all: all,
    query: query,
    path: path,
  );

  Future<GitCommitDetails> show(String hash) =>
      _client.gitShow(hash, directory: directory);

  Future<bool> commitOp(String op, String hash, {String name = ''}) {
    final short = hash.length > 7 ? hash.substring(0, 7) : hash;
    return _op(
      switch (op) {
        'revert' => 'Reverting…',
        'cherry-pick' => 'Cherry-picking…',
        'tag' => 'Tagging…',
        _ => 'Resetting…',
      },
      'commit-op',
      {'op': op, 'hash': hash, 'name': name},
      success: switch (op) {
        'revert' => 'Reverted $short',
        'cherry-pick' => 'Cherry-picked $short',
        'tag' => 'Tagged $name',
        _ => 'Reset to $short',
      },
    );
  }

  // --- stash ---------------------------------------------------------------------------

  Future<bool> stash(
    String op, {
    int index = 0,
    String message = '',
    bool includeUntracked = true,
  }) => run(
    switch (op) {
      'push' => 'Stashing…',
      'pop' => 'Popping stash…',
      'apply' => 'Applying stash…',
      _ => 'Dropping stash…',
    },
    (c, dir) async {
      final reply = await c.gitOp('stash', {
        'op': op,
        'index': index,
        'message': message,
        'includeUntracked': includeUntracked,
      }, directory: dir);
      state = state.copyWith(stashes: GitStashEntry.listFrom(reply));
    },
  );

  // --- conflicts -----------------------------------------------------------------------

  Future<bool> conflict(String op, {String path = ''}) {
    final operation = state.operation;
    return _op(
      switch (op) {
        'abort' => 'Aborting…',
        'continue' => 'Continuing…',
        _ => 'Resolving…',
      },
      'conflict',
      {'op': op, 'path': path},
      success: switch (op) {
        'abort' => 'Aborted $operation',
        'continue' => 'Completed $operation',
        _ => null,
      },
    );
  }
}

final gitProvider = NotifierProvider.autoDispose<GitNotifier, GitState>(
  GitNotifier.new,
);

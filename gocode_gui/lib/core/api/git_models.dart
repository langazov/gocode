/// Wire shapes of the `/api/vcs/git/*` Source Control routes
/// (internal/server/git.go over internal/vcs/gitops). Field names follow
/// the Go JSON tags.
library;

List<T> _list<T>(Object? raw, T Function(Map<String, dynamic>) parse) => [
  for (final item in (raw as List?) ?? const [])
    if (item is Map<String, dynamic>) parse(item),
];

List<String> _strings(Object? raw) => [
  for (final item in (raw as List?) ?? const [])
    if (item is String) item,
];

int _int(Object? raw) => (raw as num?)?.toInt() ?? 0;

/// `git status` plus the repository state the panel needs.
class GitStatus {
  const GitStatus({
    this.isRepo = false,
    this.root = '',
    this.branch = '',
    this.detached = false,
    this.head = '',
    this.hasCommits = false,
    this.upstream = '',
    this.ahead = 0,
    this.behind = 0,
    this.operation = '',
    this.stashCount = 0,
    this.lastCommitMessage = '',
    this.remotes = const [],
    this.files = const [],
  });

  factory GitStatus.fromJson(Map<String, dynamic> json) => GitStatus(
    isRepo: json['isRepo'] as bool? ?? false,
    root: json['root'] as String? ?? '',
    branch: json['branch'] as String? ?? '',
    detached: json['detached'] as bool? ?? false,
    head: json['head'] as String? ?? '',
    hasCommits: json['hasCommits'] as bool? ?? false,
    upstream: json['upstream'] as String? ?? '',
    ahead: _int(json['ahead']),
    behind: _int(json['behind']),
    operation: json['operation'] as String? ?? '',
    stashCount: _int(json['stashCount']),
    lastCommitMessage: json['lastCommitMessage'] as String? ?? '',
    remotes: _strings(json['remotes']),
    files: _list(json['files'], GitFileStatus.fromJson),
  );

  final bool isRepo;

  /// Repository top level; file paths are relative to it.
  final String root;

  /// "HEAD" when detached.
  final String branch;
  final bool detached;

  /// Short hash of HEAD.
  final String head;
  final bool hasCommits;
  final String upstream;
  final int ahead;
  final int behind;

  /// "", merge, rebase, cherry-pick or revert.
  final String operation;
  final int stashCount;
  final String lastCommitMessage;
  final List<String> remotes;
  final List<GitFileStatus> files;
}

/// One status entry. A path may appear twice: staged and in the worktree.
class GitFileStatus {
  const GitFileStatus({
    required this.path,
    this.origPath = '',
    required this.status,
    this.staged = false,
  });

  factory GitFileStatus.fromJson(Map<String, dynamic> json) => GitFileStatus(
    path: json['path'] as String? ?? '',
    origPath: json['origPath'] as String? ?? '',
    status: json['status'] as String? ?? '',
    staged: json['staged'] as bool? ?? false,
  );

  final String path;

  /// Source of a rename or copy.
  final String origPath;

  /// M A D R C ? U
  final String status;
  final bool staged;
}

class GitBranches {
  const GitBranches({
    this.current = '',
    this.branches = const [],
    this.remote = const [],
  });

  factory GitBranches.fromJson(Map<String, dynamic> json) => GitBranches(
    current: json['current'] as String? ?? '',
    branches: _list(json['branches'], GitBranchInfo.fromJson),
    remote: _strings(json['remote']),
  );

  final String current;

  /// Local branches, most recently committed first.
  final List<GitBranchInfo> branches;

  /// Remote branches ("origin/main").
  final List<String> remote;

  Iterable<String> get local => branches.map((b) => b.name);
}

class GitBranchInfo {
  const GitBranchInfo({
    required this.name,
    this.upstream = '',
    this.lastCommit = '',
    this.ahead = 0,
    this.behind = 0,
    this.lastCommitTime = 0,
    this.current = false,
  });

  factory GitBranchInfo.fromJson(Map<String, dynamic> json) => GitBranchInfo(
    name: json['name'] as String? ?? '',
    upstream: json['upstream'] as String? ?? '',
    lastCommit: json['lastCommit'] as String? ?? '',
    ahead: _int(json['ahead']),
    behind: _int(json['behind']),
    lastCommitTime: _int(json['lastCommitTime']),
    current: json['current'] as bool? ?? false,
  );

  final String name;
  final String upstream;

  /// Subject of the tip commit.
  final String lastCommit;
  final int ahead;
  final int behind;

  /// Unix seconds.
  final int lastCommitTime;
  final bool current;
}

class GitCommitInfo {
  const GitCommitInfo({
    required this.hash,
    required this.shortHash,
    this.author = '',
    this.email = '',
    this.subject = '',
    this.parents = const [],
    this.refs = const [],
    this.time = 0,
  });

  factory GitCommitInfo.fromJson(Map<String, dynamic> json) => GitCommitInfo(
    hash: json['hash'] as String? ?? '',
    shortHash: json['shortHash'] as String? ?? '',
    author: json['author'] as String? ?? '',
    email: json['email'] as String? ?? '',
    subject: json['subject'] as String? ?? '',
    parents: _strings(json['parents']),
    refs: _strings(json['refs']),
    time: _int(json['time']),
  );

  final String hash;
  final String shortHash;
  final String author;
  final String email;
  final String subject;
  final List<String> parents;

  /// Decorations: "HEAD -> main", "origin/main", "tag: v1".
  final List<String> refs;

  /// Unix seconds.
  final int time;
}

class GitCommitDetails {
  const GitCommitDetails({
    required this.commit,
    this.body = '',
    this.files = const [],
    this.additions = 0,
    this.deletions = 0,
  });

  factory GitCommitDetails.fromJson(Map<String, dynamic> json) =>
      GitCommitDetails(
        commit: GitCommitInfo.fromJson(
          json['commit'] as Map<String, dynamic>? ?? const {},
        ),
        body: json['body'] as String? ?? '',
        files: _list(json['files'], GitFileStatus.fromJson),
        additions: _int(json['additions']),
        deletions: _int(json['deletions']),
      );

  final GitCommitInfo commit;
  final String body;
  final List<GitFileStatus> files;
  final int additions;
  final int deletions;
}

class GitStashEntry {
  const GitStashEntry({required this.index, this.message = '', this.time = 0});

  factory GitStashEntry.fromJson(Map<String, dynamic> json) => GitStashEntry(
    index: _int(json['index']),
    message: json['message'] as String? ?? '',
    time: _int(json['time']),
  );

  final int index;
  final String message;

  /// Unix seconds.
  final int time;

  static List<GitStashEntry> listFrom(Map<String, dynamic> json) =>
      _list(json['entries'], GitStashEntry.fromJson);
}

/// One line of an AI commit-message draft: the message so far, then a
/// final event with [done] and the cleaned message or [error].
class CommitMessageDraft {
  const CommitMessageDraft({
    this.text = '',
    this.model = '',
    this.done = false,
    this.error = '',
  });

  factory CommitMessageDraft.fromJson(Map<String, dynamic> json) =>
      CommitMessageDraft(
        text: json['text'] as String? ?? '',
        model: json['model'] as String? ?? '',
        done: json['done'] as bool? ?? false,
        error: json['error'] as String? ?? '',
      );

  final String text;

  /// "provider/model" that wrote it.
  final String model;
  final bool done;
  final String error;
}

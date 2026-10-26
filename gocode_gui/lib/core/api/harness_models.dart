/// Harness² wire types (documentation/HARNESS2_GUI.md §4.1).
///
/// Hand-written immutable mirrors of the Go types in internal/harness
/// (service.go, run.go, library.go, cloud.go) — the TUI's client mirror in
/// internal/tui/client/client.go is the reference. The JSON is the contract.
library;

/// One label's progress in a run (harness.Row).
class HarnessRow {
  const HarnessRow({
    required this.label,
    required this.phase,
    required this.state,
    required this.costUSD,
    this.detail = '',
    this.sessionID = '',
    this.editSessionID = '',
    this.retries = 0,
  });

  factory HarnessRow.fromJson(Map<String, dynamic> json) => HarnessRow(
    label: json['label'] as String? ?? '',
    phase: json['phase'] as String? ?? '',
    state: json['state'] as String? ?? '',
    costUSD: (json['costUSD'] as num?)?.toDouble() ?? 0,
    detail: json['detail'] as String? ?? '',
    sessionID: json['sessionID'] as String? ?? '',
    editSessionID: json['editSessionID'] as String? ?? '',
    retries: (json['retries'] as num?)?.toInt() ?? 0,
  );

  final String label;
  final String phase; // base, probe, compose, select, reflect
  final String state; // pending, editing, edited, rolling, ok, failed

  final double costUSD;
  final String detail;

  /// The label's rollout session; [editSessionID] its latest improver one.
  final String sessionID;
  final String editSessionID;
  final int retries;

  /// A row settles when its rollout (or, for select/experience, its stage)
  /// has ended one way or the other.
  bool get isSettled => state == 'ok' || state == 'failed';
}

/// A run, live or finished (harness.RunState).
class HarnessRun {
  const HarnessRun({
    required this.id,
    required this.task,
    required this.domain,
    required this.k,
    required this.mode,
    required this.status,
    required this.budget,
    this.phase = '',
    this.backend = '',
    this.reason = '',
    this.rows = const [],
    this.costUSD = 0,
    this.startedAt = 0,
    this.adopted = '',
    this.why = '',
    this.lesson = '',
    this.error = '',
    this.workspace = '',
    this.prompt = '',
  });

  factory HarnessRun.fromJson(Map<String, dynamic> json) => HarnessRun(
    id: json['id'] as String? ?? '',
    task: json['task'] as String? ?? '',
    domain: json['domain'] as String? ?? '',
    k: (json['k'] as num?)?.toInt() ?? 1,
    mode: json['mode'] as String? ?? 'parallel',
    status: json['status'] as String? ?? '',
    phase: json['phase'] as String? ?? '',
    backend: json['backend'] as String? ?? '',
    reason: json['reason'] as String? ?? '',
    rows: [
      for (final row in (json['rows'] as List?) ?? const <Object>[])
        if (row is Map<String, dynamic>) HarnessRow.fromJson(row),
    ],
    costUSD: (json['costUSD'] as num?)?.toDouble() ?? 0,
    startedAt: (json['startedAt'] as String?) != null
        ? DateTime.tryParse(json['startedAt'] as String)
                  ?.millisecondsSinceEpoch ??
              0
        : 0,
    adopted: json['adopted'] as String? ?? '',
    why: json['why'] as String? ?? '',
    lesson: json['lesson'] as String? ?? '',
    error: json['error'] as String? ?? '',
    budget: json['budget'] as String? ?? '',
    workspace: json['workspace'] as String? ?? '',
    prompt: json['prompt'] as String? ?? '',
  );

  final String id;
  final String task;
  final String domain;
  final int k;
  final String mode;
  final String
  status; // running, ok, partial, base-failed, failed, cancelled, planned
  final String phase;
  final String backend;
  final String reason;
  final List<HarnessRow> rows;
  final double costUSD;
  final int startedAt; // epoch millis
  final String adopted;
  final String why;
  final String lesson;
  final String error;
  final String budget;
  final String workspace;
  final String prompt;

  bool get isRunning => status == 'running';
  bool get isFinished => status != 'running' && status != 'planned';

  int get rowsDone => rows.where((r) => r.isSettled).length;

  /// The display order rows move through, matching the TUI's phase labels.
  static const phaseOrder = ['base', 'probe', 'compose', 'select', 'reflect'];

  /// Rows grouped by phase in run order; a run always groups this way, so
  /// widgets never re-sort.
  Map<String, List<HarnessRow>> get rowsByPhase {
    final out = <String, List<HarnessRow>>{};
    for (final phase in phaseOrder) {
      final inPhase = rows.where((r) => r.phase == phase).toList();
      if (inPhase.isNotEmpty) out[phase] = inPhase;
    }
    final rest = rows.where((r) => !phaseOrder.contains(r.phase)).toList();
    if (rest.isNotEmpty) out['other'] = rest;
    return out;
  }
}

/// A harness tree diff (harness.Diff).
class HarnessFiles {
  const HarnessFiles({
    this.added = const [],
    this.removed = const [],
    this.modified = const [],
    this.alwaysOnCharsDelta = 0,
  });

  factory HarnessFiles.fromJson(Map<String, dynamic> json) => HarnessFiles(
    added: [
      for (final v in (json['added'] as List?) ?? const <Object>[])
        if (v is String) v,
    ],
    removed: [
      for (final v in (json['removed'] as List?) ?? const <Object>[])
        if (v is String) v,
    ],
    modified: [
      for (final v in (json['modified'] as List?) ?? const <Object>[])
        if (v is String) v,
    ],
    alwaysOnCharsDelta: (json['alwaysOnCharsDelta'] as num?)?.toInt() ?? 0,
  );

  final List<String> added;
  final List<String> removed;
  final List<String> modified;
  final int alwaysOnCharsDelta;

  bool get unchanged => added.isEmpty && removed.isEmpty && modified.isEmpty;
}

/// One label's harness and rollout (harness.StageDetail).
class HarnessStage {
  const HarnessStage({
    required this.label,
    required this.diff,
    required this.files,
    required this.ok,
    required this.delivered,
    required this.costUSD,
    this.notes = '',
    this.error = '',
    this.sessionID = '',
    this.deliverable = '',
  });

  factory HarnessStage.fromJson(Map<String, dynamic> json) => HarnessStage(
    label: json['label'] as String? ?? '',
    diff: json['diff'] as String? ?? '',
    files: json['files'] is Map<String, dynamic>
        ? HarnessFiles.fromJson(json['files'] as Map<String, dynamic>)
        : const HarnessFiles(),
    notes: json['notes'] as String? ?? '',
    error: json['error'] as String? ?? '',
    sessionID: json['sessionID'] as String? ?? '',
    ok: json['ok'] as bool? ?? false,
    delivered: json['delivered'] as bool? ?? false,
    deliverable: json['deliverable'] as String? ?? '',
    costUSD: (json['costUSD'] as num?)?.toDouble() ?? 0,
  );

  final String label;
  final String diff;
  final HarnessFiles files;
  final String notes;
  final String error;
  final String sessionID;
  final bool ok;
  final bool delivered;

  /// The rollout's product: its final message and its workspace diff in a
  /// fenced ```diff block.
  final String deliverable;
  final double costUSD;

  /// The workspace diff extracted from the deliverable's diff fence.
  String get workspaceDiff {
    final match = RegExp(
      r'```diff\n(.*?)```',
      dotAll: true,
    ).firstMatch(deliverable);
    return match?.group(1)?.trimRight() ?? '';
  }

  /// The deliverable's prose: everything before the changes heading.
  String get finalMessage {
    final cut = deliverable.indexOf('# Changes to the workspace');
    return (cut > 0 ? deliverable.substring(0, cut) : deliverable).trim();
  }
}

/// What publishing skills to the Library did or would do
/// (harness.PublishReport).
class HarnessPublish {
  const HarnessPublish({
    this.newSkills = const [],
    this.changed = const [],
    this.unchanged = const [],
    this.blocked = const [],
  });

  factory HarnessPublish.fromJson(Map<String, dynamic> json) {
    List<String> strings(String key) => [
      for (final v in (json[key] as List?) ?? const <Object>[])
        if (v is String) v,
    ];
    return HarnessPublish(
      newSkills: strings('new'),
      changed: strings('changed'),
      unchanged: strings('unchanged'),
      blocked: strings('blocked'),
    );
  }

  final List<String> newSkills;
  final List<String> changed;
  final List<String> unchanged;

  /// Changed skills left alone because force was not set.
  final List<String> blocked;
}

/// An install's result or plan (harness.InstallResult).
class HarnessInstall {
  const HarnessInstall({
    required this.files,
    required this.conflict,
    required this.written,
    this.target = '',
    this.collisions = const [],
    this.warnings = const [],
    this.library,
  });

  factory HarnessInstall.fromJson(Map<String, dynamic> json) => HarnessInstall(
    target: json['target'] as String? ?? '',
    files: json['files'] is Map<String, dynamic>
        ? HarnessFiles.fromJson(json['files'] as Map<String, dynamic>)
        : const HarnessFiles(),
    conflict: json['conflict'] as bool? ?? false,
    collisions: [
      for (final v in (json['collisions'] as List?) ?? const <Object>[])
        if (v is String) v,
    ],
    warnings: [
      for (final v in (json['warnings'] as List?) ?? const <Object>[])
        if (v is String) v,
    ],
    library: json['library'] is Map<String, dynamic>
        ? HarnessPublish.fromJson(json['library'] as Map<String, dynamic>)
        : null,
    written: json['written'] as bool? ?? false,
  );

  final String target;
  final HarnessFiles files;
  final bool conflict;
  final List<String> collisions;
  final List<String> warnings;
  final HarnessPublish? library;
  final bool written;
}

/// One experience-library entry in a listing (harness.EntryInfo).
class HarnessEntryInfo {
  const HarnessEntryInfo({
    required this.domain,
    required this.task,
    required this.components,
    this.cue = '',
    this.pending = false,
  });

  factory HarnessEntryInfo.fromJson(Map<String, dynamic> json) =>
      HarnessEntryInfo(
        domain: json['domain'] as String? ?? '',
        task: json['task'] as String? ?? '',
        cue: json['cue'] as String? ?? '',
        components: json['components'] as String? ?? '',
        pending: json['pending'] as bool? ?? false,
      );

  final String domain;
  final String task;
  final String cue;
  final String components;
  final bool pending;
}

/// GET /api/harness/library.
class HarnessLibrary {
  const HarnessLibrary({
    required this.backend,
    required this.reason,
    required this.domains,
  });

  factory HarnessLibrary.fromJson(Map<String, dynamic> json) => HarnessLibrary(
    backend: json['backend'] as String? ?? '',
    reason: json['reason'] as String? ?? '',
    domains: {
      for (final d in (json['domains'] as List?) ?? const <Object>[])
        if (d is Map<String, dynamic>)
          (d['domain'] as String? ?? ''): [
            for (final e in (d['entries'] as List?) ?? const <Object>[])
              if (e is Map<String, dynamic>) HarnessEntryInfo.fromJson(e),
          ],
    },
  );

  final String backend;
  final String reason;
  final Map<String, List<HarnessEntryInfo>> domains;
}

/// An entry's lesson (harness.Experience).
class HarnessLesson {
  const HarnessLesson({
    required this.cue,
    required this.lesson,
    required this.evidence,
    this.confidence = '',
    this.edits = '',
  });

  factory HarnessLesson.fromJson(Map<String, dynamic> json) => HarnessLesson(
    cue: json['cue'] as String? ?? '',
    lesson: json['lesson'] as String? ?? '',
    evidence: json['evidence'] as String? ?? '',
    confidence: json['confidence'] as String? ?? '',
    edits: json['edits'] as String? ?? '',
  );

  final String cue;
  final String lesson;
  final String evidence;
  final String confidence;
  final String edits;
}

/// One library entry with its full tree (GET /api/harness/library/{d}/{t}).
class HarnessEntry {
  const HarnessEntry({
    required this.domain,
    required this.task,
    required this.label,
    required this.lesson,
    this.why = '',
    this.tree = const {},
  });

  factory HarnessEntry.fromJson(Map<String, dynamic> json) {
    final entry = json['entry'];
    final e = entry is Map<String, dynamic> ? entry : const <String, dynamic>{};
    final tree = json['tree'];
    return HarnessEntry(
      domain: e['domain'] as String? ?? '',
      task: e['task'] as String? ?? '',
      label: e['label'] as String? ?? '',
      why: e['why'] as String? ?? '',
      lesson: e['lesson'] is Map<String, dynamic>
          ? HarnessLesson.fromJson(e['lesson'] as Map<String, dynamic>)
          : const HarnessLesson(cue: '', lesson: '', evidence: ''),
      tree: tree is Map<String, dynamic>
          ? tree.map((k, v) => MapEntry(k, v is String ? v : ''))
          : const {},
    );
  }

  final String domain;
  final String task;
  final String label;
  final String why;
  final HarnessLesson lesson;

  /// Every harness file, path → content.
  final Map<String, String> tree;

  /// Tree paths with content, sorted — empty files are skipped, as the
  /// TUI's harnessEntryLines does.
  List<String> get paths =>
      (tree.keys.where((p) => tree[p]!.trim().isNotEmpty).toList()..sort());
}

/// One offered (mode, k) pair (harness.Budget), from GET
/// /api/harness/budgets.
class HarnessBudget {
  const HarnessBudget({
    required this.mode,
    required this.k,
    required this.rollouts,
    required this.edits,
    required this.description,
  });

  factory HarnessBudget.fromJson(Map<String, dynamic> json) => HarnessBudget(
    mode: json['mode'] as String? ?? 'parallel',
    k: (json['k'] as num?)?.toInt() ?? 1,
    rollouts: (json['rollouts'] as num?)?.toInt() ?? 0,
    edits: (json['edits'] as num?)?.toInt() ?? 0,
    description: json['description'] as String? ?? '',
  );

  final String mode;
  final int k;
  final int rollouts;
  final int edits;
  final String description;

  /// The picker's value key, the TUI's `mode-k`.
  String get key => '$mode-$k';
}

/// The body of POST /api/harness/run; [dryRun] returns the plan.
class HarnessStartRequest {
  const HarnessStartRequest({
    required this.prompt,
    this.id,
    this.domain,
    this.workspace,
    this.k,
    this.mode,
    this.model,
    this.improverModel,
    this.library,
    this.crossDomain,
    this.dryRun = false,
  });

  final String prompt;
  final String? id;
  final String? domain;
  final String? workspace;
  final int? k;
  final String? mode;
  final String? model;
  final String? improverModel;
  final String? library;
  final bool? crossDomain;
  final bool dryRun;

  Map<String, Object?> toJson() => {
    'prompt': prompt,
    if (id != null && id!.isNotEmpty) 'id': id,
    if (domain != null && domain!.isNotEmpty) 'domain': domain,
    if (workspace != null && workspace!.isNotEmpty) 'workspace': workspace,
    if (k != null) 'k': k,
    if (mode != null && mode!.isNotEmpty) 'mode': mode,
    if (model != null && model!.isNotEmpty) 'model': model,
    if (improverModel != null && improverModel!.isNotEmpty)
      'improverModel': improverModel,
    if (library != null && library!.isNotEmpty) 'library': library,
    if (crossDomain ?? false) 'crossDomain': true,
    if (dryRun) 'dryRun': true,
  };
}

/// A 409 from install whose body carries the conflicting result: the dialog
/// can show what conflicted instead of a bare error.
class HarnessInstallConflict implements Exception {
  const HarnessInstallConflict(this.message, this.result);

  final String message;

  /// The dry-run result naming the conflict (harness.InstallResult).
  final HarnessInstall? result;

  @override
  String toString() => message;
}

/// `$1.25`, the money format the TUI's formatMoney uses.
String harnessMoney(double value) {
  if (value == 0) return '—';
  return '\$${value.toStringAsFixed(2)}';
}

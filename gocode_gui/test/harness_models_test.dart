import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/api/harness_models.dart';

/// The wire shapes the gocode server's tests produce (internal/server/
/// harness_test.go, internal/harness): a change there breaks a test here,
/// not the app.
Map<String, dynamic> fixture(String name) =>
    jsonDecode(File('test/fixtures/harness/$name.json').readAsStringSync())
        as Map<String, dynamic>;

List<dynamic> fixtureList(String name) =>
    jsonDecode(File('test/fixtures/harness/$name.json').readAsStringSync())
        as List<dynamic>;

void main() {
  test('HarnessRun.fromJson reads every field of a finished run', () {
    final run = HarnessRun.fromJson(fixture('run'));
    expect(run.id, 'fix-login--k1-parallel');
    expect(run.task, 'fix-login');
    expect(run.domain, 'auth');
    expect(run.k, 1);
    expect(run.mode, 'parallel');
    expect(run.status, 'ok');
    expect(run.backend, 'local');
    expect(run.reason, 'harness.library is local');
    expect(run.costUSD, 1.25);
    expect(run.adopted, 'agg1');
    expect(run.why, contains('redirect test'));
    expect(run.lesson, 'a login redirect');
    expect(run.budget, contains('4 rollouts'));
    expect(run.workspace, '/home/dev/project');
    expect(run.prompt, startsWith('Fix the login redirect'));
    expect(run.startedAt, DateTime.parse('2025-06-01T10:00:00Z')
        .millisecondsSinceEpoch);
    expect(run.isRunning, isFalse);
    expect(run.isFinished, isTrue);
  });

  test('HarnessRun.rowsByPhase groups in run order', () {
    final run = HarnessRun.fromJson(fixture('run'));
    final phases = run.rowsByPhase.keys.toList();
    expect(phases, ['base', 'probe', 'compose', 'reflect']);
    expect(run.rowsByPhase['base']!.single.label, 'base1');
    expect(
      run.rowsByPhase['probe']!.map((r) => r.label),
      ['inv1', 'dom1'],
    );
    expect(run.rowsByPhase['compose']!.single.label, 'agg1');
    // The default has no select row (k=1); a k>1 run would carry one.
    expect(run.rowsByPhase.containsKey('select'), isFalse);
  });

  test('HarnessRow reads sessions, retries and settles', () {
    final run = HarnessRun.fromJson(fixture('run'));
    final agg1 = run.rowsByPhase['compose']!.single;
    expect(agg1.sessionID, 'ses_agg1');
    expect(agg1.editSessionID, 'ses_edit_agg1');
    expect(agg1.retries, 1);
    expect(agg1.isSettled, isTrue);
    expect(
      HarnessRow(
        label: 'x',
        phase: 'probe',
        state: 'editing',
        costUSD: 0,
      ).isSettled,
      isFalse,
    );
    expect(run.rowsDone, run.rows.length);
  });

  test('a running run is not finished and counts its settled rows', () {
    final run = HarnessRun.fromJson({
      ...fixture('run'),
      'status': 'running',
      'adopted': '',
      'why': '',
      'rows': [
        {'label': 'base1', 'phase': 'base', 'state': 'ok', 'costUSD': 0.4},
        {
          'label': 'inv1',
          'phase': 'probe',
          'state': 'editing',
          'costUSD': 0,
        },
      ],
    });
    expect(run.isRunning, isTrue);
    expect(run.isFinished, isFalse);
    expect(run.rowsDone, 1);
    expect(run.rows.length, 2);
  });

  test('HarnessStage parses files, notes and the deliverable', () {
    final stage = HarnessStage.fromJson(fixture('stage'));
    expect(stage.label, 'agg1');
    expect(stage.files.added, ['systemprompt.md']);
    expect(stage.files.modified, ['tool_guidance.md']);
    expect(stage.files.alwaysOnCharsDelta, 42);
    expect(stage.files.unchanged, isFalse);
    expect(stage.ok, isTrue);
    expect(stage.delivered, isTrue);
    expect(stage.sessionID, 'ses_agg1');
    // The workspace diff comes out of the deliverable's diff fence.
    expect(stage.workspaceDiff, contains('+notes'));
    expect(stage.workspaceDiff, isNot(contains('```')));
    // The prose is everything before the changes heading.
    expect(stage.finalMessage, contains('Created notes.txt.'));
    expect(stage.finalMessage, isNot(contains('# Changes')));
  });

  test('HarnessInstall parses conflict, collisions and the publish report',
      () {
    final install = HarnessInstall.fromJson(fixture('install'));
    expect(install.target, '/home/dev/project/.gocode/harness');
    expect(install.conflict, isTrue);
    expect(install.collisions, ['a-skill.md']);
    expect(install.warnings, isNotEmpty);
    expect(install.written, isFalse);
    expect(install.files.removed, ['old.md']);
    final library = install.library!;
    expect(library.newSkills, ['fix-login-a']);
    expect(library.changed, ['auth-b']);
    expect(library.unchanged, ['auth-c']);
    expect(library.blocked, ['auth-b']);
  });

  test('HarnessLibrary groups entries by domain in order', () {
    final library = HarnessLibrary.fromJson(fixture('library'));
    expect(library.backend, 'split');
    expect(library.reason, 'library-plugin loaded and signed in');
    final auth = library.domains['auth']!;
    expect(auth.length, 2);
    expect(auth.first.task, 'fix-login');
    expect(auth.first.cue, 'a login redirect');
    expect(auth.first.pending, isFalse);
    expect(auth.last.pending, isTrue);
  });

  test('HarnessEntry reads the lesson, why and the tree', () {
    final entry = HarnessEntry.fromJson(fixture('entry'));
    expect(entry.domain, 'auth');
    expect(entry.task, 'fix-login');
    expect(entry.label, 'agg1');
    expect(entry.why, contains('redirect test'));
    expect(entry.lesson.cue, 'a login redirect');
    expect(entry.lesson.lesson, contains('named files first'));
    expect(entry.lesson.evidence, 'inv1 created it');
    expect(entry.lesson.confidence, 'low');
    expect(entry.lesson.edits, isNotEmpty);
    // Empty tree files are skipped; the rest are sorted.
    expect(entry.paths, ['scripts/check.sh', 'systemprompt.md']);
    expect(entry.tree['systemprompt.md'], contains('Create the files'));
  });

  test('HarnessBudget parses mode, k and its price', () {
    final budgets = [
      for (final b in fixtureList('budgets'))
        HarnessBudget.fromJson(b as Map<String, dynamic>),
    ];
    expect(budgets.length, 2);
    expect(budgets.first.mode, 'parallel');
    expect(budgets.first.k, 1);
    expect(budgets.first.rollouts, 4);
    expect(budgets.first.edits, 3);
    expect(budgets.first.description, 'one Propose–Probe–Compose step');
    expect(budgets.first.key, 'parallel-1');
    expect(budgets.last.key, 'sequential-2');
  });

  test('HarnessStartRequest.toJson only sends what is set', () {
    const minimal = HarnessStartRequest(prompt: 'Fix the login');
    expect(minimal.toJson(), {'prompt': 'Fix the login'});

    const full = HarnessStartRequest(
      prompt: 'Fix the login',
      id: 'fix-login',
      domain: 'auth',
      workspace: '/project',
      k: 2,
      mode: 'sequential',
      model: 'anthropic/claude-sonnet-4-5',
      improverModel: 'openai/gpt-mini',
      library: 'cloud',
      crossDomain: true,
      dryRun: true,
    );
    expect(full.toJson(), {
      'prompt': 'Fix the login',
      'id': 'fix-login',
      'domain': 'auth',
      'workspace': '/project',
      'k': 2,
      'mode': 'sequential',
      'model': 'anthropic/claude-sonnet-4-5',
      'improverModel': 'openai/gpt-mini',
      'library': 'cloud',
      'crossDomain': true,
      'dryRun': true,
    });
  });

  test('harnessMoney formats as the TUI does', () {
    expect(harnessMoney(1.254), r'$1.25');
    expect(harnessMoney(0), '—');
    expect(harnessMoney(12), r'$12.00');
  });
}

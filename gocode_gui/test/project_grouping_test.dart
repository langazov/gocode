import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/features/sidebar/project_grouping.dart';
import 'package:gocode_gui/features/sidebar/projects.dart';

Session _session(
  String id,
  String directory, {
  int updated = 0,
  String? parent,
}) => Session(
  id: id,
  title: id,
  directory: directory,
  parentID: parent,
  timeCreated: updated,
  timeUpdated: updated,
);

Project _project(String id, String directory, {int created = 0}) =>
    Project(id: id, name: id, directory: directory, timeCreated: created);

void main() {
  group('groupByProject', () {
    test('sessions matching a project directory land in its group', () {
      final projects = [_project('p1', '/work/app')];
      final sessions = [
        _session('s1', '/work/app', updated: 1),
        _session('s2', '/work/other', updated: 2),
      ];
      final result = groupByProject(projects, sessions);
      expect(result.projects, hasLength(1));
      expect(result.projects.single.sessions.map((s) => s.id), ['s1']);
      expect(result.unassigned.map((s) => s.id), ['s2']);
    });

    test('sessions within a project are sorted most-recent first', () {
      final projects = [_project('p1', '/work/app')];
      final sessions = [
        _session('older', '/work/app', updated: 1),
        _session('newer', '/work/app', updated: 2),
      ];
      final result = groupByProject(projects, sessions);
      expect(result.projects.single.sessions.map((s) => s.id), [
        'newer',
        'older',
      ]);
    });

    test('a project with no matching sessions still gets a group', () {
      final projects = [_project('p1', '/work/empty')];
      final result = groupByProject(projects, const []);
      expect(result.projects, hasLength(1));
      expect(result.projects.single.sessions, isEmpty);
    });

    test('subagent sessions are dropped from both projects and unassigned', () {
      final projects = [_project('p1', '/work/app')];
      final sessions = [
        _session('s1', '/work/app', updated: 1),
        _session('sub', '/work/app', updated: 2, parent: 's1'),
        _session('sub2', '/other', updated: 2, parent: 's1'),
      ];
      final result = groupByProject(projects, sessions);
      expect(result.projects.single.sessions.map((s) => s.id), ['s1']);
      expect(result.unassigned, isEmpty);
    });

    test('with no projects, every session is unassigned', () {
      final sessions = [_session('s1', '/work/app', updated: 1)];
      final result = groupByProject(const [], sessions);
      expect(result.projects, isEmpty);
      expect(result.unassigned.map((s) => s.id), ['s1']);
    });

    test('project group order follows the given project list', () {
      final projects = [_project('p2', '/b'), _project('p1', '/a')];
      final result = groupByProject(projects, const []);
      expect(result.projects.map((g) => g.project.id), ['p2', 'p1']);
    });
  });
}

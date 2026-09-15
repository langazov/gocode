import '../../core/api/models.dart';
import 'projects.dart';

/// A project paired with the sessions whose directory exactly matches it,
/// most-recently-active first.
class ProjectGroup {
  const ProjectGroup(this.project, this.sessions);

  final Project project;
  final List<Session> sessions;
}

/// Splits [sessions] into per-project groups (by exact directory match,
/// [projects] order preserved) and the sessions that don't belong to any
/// known project — the sidebar's two tabs. Subagent sessions are dropped,
/// same as the flat history list they used to feed.
({List<ProjectGroup> projects, List<Session> unassigned}) groupByProject(
  List<Project> projects,
  List<Session> sessions,
) {
  final byDirectory = <String, List<Session>>{};
  final unassigned = <Session>[];
  for (final session in sessions) {
    if (session.isSubagent) continue;
    final belongs = projects.any((p) => p.directory == session.directory);
    if (belongs) {
      byDirectory.putIfAbsent(session.directory, () => []).add(session);
    } else {
      unassigned.add(session);
    }
  }
  int byRecency(Session a, Session b) => b.timeUpdated.compareTo(a.timeUpdated);
  final groups = [
    for (final project in projects)
      ProjectGroup(
        project,
        (byDirectory[project.directory] ?? const <Session>[]).toList()
          ..sort(byRecency),
      ),
  ];
  unassigned.sort(byRecency);
  return (projects: groups, unassigned: unassigned);
}

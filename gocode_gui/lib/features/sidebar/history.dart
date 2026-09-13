import '../../core/api/models.dart';

/// Sessions that share a "last active" bucket in the sidebar.
class SessionGroup {
  SessionGroup(this.label, this.sessions);

  final String label;
  final List<Session> sessions;
}

/// Buckets [sessions] (newest first) by when they were last active, relative
/// to [now]: Today, Yesterday, Previous 7 days, Previous 30 days, Older.
List<SessionGroup> groupSessions(List<Session> sessions, DateTime now) {
  final today = DateTime(now.year, now.month, now.day);
  String bucket(int millis) {
    final at = DateTime.fromMillisecondsSinceEpoch(millis);
    final day = DateTime(at.year, at.month, at.day);
    // Rounded, so a DST day of 23 or 25 hours still counts as one day.
    final days = (today.difference(day).inHours / 24).round();
    if (days <= 0) return 'Today';
    if (days == 1) return 'Yesterday';
    if (days < 7) return 'Previous 7 days';
    if (days < 30) return 'Previous 30 days';
    return 'Older';
  }

  final groups = <SessionGroup>[];
  for (final session in sessions) {
    final label = bucket(session.timeUpdated);
    if (groups.isEmpty || groups.last.label != label) {
      groups.add(SessionGroup(label, []));
    }
    groups.last.sessions.add(session);
  }
  return groups;
}

/// A compact age for a history row: now, 5m, 3h, 2d, 4w, then a date.
String shortAge(int millis, DateTime now) {
  if (millis == 0) return '';
  final at = DateTime.fromMillisecondsSinceEpoch(millis);
  final age = now.difference(at);
  if (age.inMinutes < 1) return 'now';
  if (age.inHours < 1) return '${age.inMinutes}m';
  if (age.inDays < 1) return '${age.inHours}h';
  if (age.inDays < 7) return '${age.inDays}d';
  if (age.inDays < 30) return '${age.inDays ~/ 7}w';
  const months = [
    'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', //
    'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
  ];
  return '${months[at.month - 1]} ${at.day}';
}

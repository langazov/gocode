import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/api/models.dart';
import 'package:gocode_gui/features/account/daily_bars.dart';
import 'package:gocode_gui/features/sidebar/history.dart';

Session _session(String id, DateTime updated) => Session(
  id: id,
  title: id,
  directory: '/project',
  timeCreated: updated.millisecondsSinceEpoch,
  timeUpdated: updated.millisecondsSinceEpoch,
);

void main() {
  test('groups sessions by when they were last active', () {
    final now = DateTime(2026, 9, 13, 15);
    final sessions = [
      _session('this-morning', DateTime(2026, 9, 13, 8)),
      _session('last-night', DateTime(2026, 9, 12, 23)),
      _session('monday', DateTime(2026, 9, 8, 10)),
      _session('august', DateTime(2026, 8, 20)),
      _session('spring', DateTime(2026, 4, 1)),
    ];
    final groups = groupSessions(sessions, now);
    expect(groups.map((g) => g.label), [
      'Today',
      'Yesterday',
      'Previous 7 days',
      'Previous 30 days',
      'Older',
    ]);
    expect(groups.map((g) => g.sessions.single.id), [
      'this-morning',
      'last-night',
      'monday',
      'august',
      'spring',
    ]);
  });

  test('keeps consecutive sessions of one bucket together', () {
    final now = DateTime(2026, 9, 13, 15);
    final groups = groupSessions([
      _session('a', DateTime(2026, 9, 13, 14)),
      _session('b', DateTime(2026, 9, 13, 9)),
    ], now);
    expect(groups, hasLength(1));
    expect(groups.single.sessions.map((s) => s.id), ['a', 'b']);
  });

  test('short ages', () {
    final now = DateTime(2026, 9, 13, 15);
    int ago(Duration d) => now.subtract(d).millisecondsSinceEpoch;
    expect(shortAge(ago(const Duration(seconds: 20)), now), 'now');
    expect(shortAge(ago(const Duration(minutes: 5)), now), '5m');
    expect(shortAge(ago(const Duration(hours: 3)), now), '3h');
    expect(shortAge(ago(const Duration(days: 2)), now), '2d');
    expect(shortAge(ago(const Duration(days: 15)), now), '2w');
    expect(shortAge(DateTime(2026, 3, 4).millisecondsSinceEpoch, now), 'Mar 4');
  });

  test('axis ceilings land on clean values', () {
    expect(niceCeiling(0), 1);
    expect(niceCeiling(7), 10);
    expect(niceCeiling(12), 20);
    expect(niceCeiling(2100), 2500);
    expect(niceCeiling(3400), 5000);
    expect(niceCeiling(100000), 100000);
  });
}

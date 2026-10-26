import 'package:flutter/material.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import 'timeline.dart';

/// The runner's harness marker in the timeline (internal/session
/// HarnessMessage): where the session started running under an installed
/// harness, a changed one, or none any more. A divider like the other
/// notices, titled in the accent, over one muted line saying where the
/// harness lives and what it brings; hovering shows the skills, agents and
/// full path.
class HarnessNotice extends StatelessWidget {
  const HarnessNotice({super.key, required this.data});

  final Map<String, dynamic> data;

  @override
  Widget build(BuildContext context) {
    final loaded = data['loaded'] == true;
    final title = !loaded
        ? 'Harness removed'
        : data['changed'] == true
        ? 'Harness changed'
        : 'Harness loaded';
    final color = loaded ? GC.accentText : GC.textFaint;
    final facts = loaded ? harnessFacts(data) : const <String>[];
    return Padding(
      padding: const EdgeInsets.only(bottom: 14),
      child: Tooltip(
        message: loaded
            ? _details()
            : 'This session no longer runs under a harness',
        waitDuration: const Duration(milliseconds: 500),
        child: Column(
          children: [
            Row(
              children: [
                const Expanded(child: Divider()),
                Padding(
                  padding: const EdgeInsets.symmetric(horizontal: 10),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Icon(
                        Icons.auto_fix_high_outlined,
                        size: 13,
                        color: color,
                      ),
                      const SizedBox(width: 6),
                      Text(title, style: GC.caption.copyWith(color: color)),
                    ],
                  ),
                ),
                const Expanded(child: Divider()),
              ],
            ),
            if (facts.isNotEmpty) ...[
              const SizedBox(height: 4),
              Text(
                facts.join(' · '),
                textAlign: TextAlign.center,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: GC.code.copyWith(fontSize: 11, color: GC.textFaint),
              ),
            ],
          ],
        ),
      ),
    );
  }

  String _details() {
    final lines = <String>[
      '${data['dir'] ?? ''}',
      for (final (key, label) in const [
        ('alwaysOn', 'Always on'),
        ('skills', 'Skills'),
        ('agents', 'Agents'),
      ])
        if (data[key] is List && (data[key] as List).isNotEmpty)
          '$label: ${(data[key] as List).join(', ')}',
      if (data['fingerprint'] is String) 'Fingerprint: ${data['fingerprint']}',
    ];
    return lines.join('\n');
  }
}

/// What a harness brings, place first and fingerprint last — the TUI's
/// harnessFacts.
List<String> harnessFacts(Map<String, dynamic> data) {
  final facts = <String>[harnessPlace('${data['dir'] ?? ''}')];
  final alwaysOn = data['alwaysOn'];
  if (alwaysOn is List && alwaysOn.isNotEmpty) facts.add(alwaysOn.join(', '));
  void count(Object? n, String one) {
    final v = n is List ? n.length : (n is num ? n.toInt() : 0);
    if (v == 1) facts.add('1 $one');
    if (v > 1) facts.add('$v ${one}s');
  }

  count(data['skills'], 'skill');
  count(data['agents'], 'agent');
  count(data['scripts'], 'script');
  count(data['hooks'], 'hook rule');
  final fingerprint = data['fingerprint'];
  if (fingerprint is String && fingerprint.isNotEmpty) facts.add(fingerprint);
  return facts;
}

/// An installed harness by its project-relative path; anything else as is.
String harnessPlace(String dir) {
  final slashed = dir.replaceAll('\\', '/');
  return slashed.endsWith('/.gocode/harness') ? '.gocode/harness' : slashed;
}

/// The harness the session runs under now — its newest marker, when that
/// marker says loaded — or null.
Map<String, dynamic>? activeHarness(List<TimelineItem> items) {
  for (final item in items.reversed) {
    if (item is NoticeItem && item.kind == Message.harness) {
      return item.data['loaded'] == true ? item.data : null;
    }
  }
  return null;
}

import 'package:flutter/material.dart';

import '../../app/theme.dart';

/// Source Control colors on top of the GC tokens: diff washes, file-status
/// letters and commit-graph lanes (goide's `scm.*` / `diff*` theme keys).
abstract final class GitColors {
  static const addedFg = GC.ok;
  static const removedFg = GC.downText;
  static final addedBg = GC.ok.withValues(alpha: 0.16);
  static final removedBg = GC.down.withValues(alpha: 0.16);
  static final hunkBg = GC.accent.withValues(alpha: 0.07);
  static const hunkFg = GC.accentText;
  static const emptySide = Color(0x08FFFFFF);

  static const modified = Color(0xFFD9B26F);
  static const added = GC.ok;
  static const deleted = GC.downText;
  static const renamed = Color(0xFF7FB2D9);
  static const conflict = GC.warn;
  static const remoteRef = Color(0xFF9C8FD9);

  static const _lanes = [
    GC.accent,
    Color(0xFF7FB2D9),
    GC.ok,
    Color(0xFFC88FD9),
    Color(0xFFD9B26F),
    Color(0xFF6FC2B8),
    GC.downText,
    Color(0xFF9C8FD9),
  ];

  static Color lane(int i) => _lanes[i % _lanes.length];

  static Color forStatus(String s) => switch (s) {
    'A' || '?' => added,
    'D' => deleted,
    'R' || 'C' => renamed,
    'U' => conflict,
    _ => modified, // M
  };
}

String relativeTime(int unixSeconds) {
  if (unixSeconds <= 0) return '';
  final d = DateTime.now().difference(
    DateTime.fromMillisecondsSinceEpoch(unixSeconds * 1000),
  );
  if (d.inMinutes < 1) return 'just now';
  if (d.inHours < 1) return '${d.inMinutes}m ago';
  if (d.inDays < 1) return '${d.inHours}h ago';
  if (d.inDays < 30) return '${d.inDays}d ago';
  if (d.inDays < 365) return '${d.inDays ~/ 30}mo ago';
  return '${d.inDays ~/ 365}y ago';
}

String baseName(String path) => path.split('/').last;

String dirName(String path) =>
    path.contains('/') ? path.substring(0, path.lastIndexOf('/')) : '';

/// Colored single-letter git status (M, A, D, R, U, ?).
class StatusLetter extends StatelessWidget {
  const StatusLetter(this.status, {super.key});

  final String status;

  @override
  Widget build(BuildContext context) => SizedBox(
    width: 14,
    child: Text(
      // VS Code convention: U = untracked, ! = conflict.
      status == '?' ? 'U' : (status == 'U' ? '!' : status),
      textAlign: TextAlign.center,
      style: GC.code.copyWith(
        fontSize: 12,
        fontWeight: FontWeight.w700,
        color: GitColors.forStatus(status),
      ),
    ),
  );
}

/// A small rounded label (the diff source, counts).
class GitChip extends StatelessWidget {
  const GitChip(this.label, {super.key});

  final String label;

  @override
  Widget build(BuildContext context) => Container(
    padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
    decoration: BoxDecoration(
      color: GC.surface3,
      borderRadius: BorderRadius.circular(10),
    ),
    child: Text(
      label,
      style: const TextStyle(
        fontFamily: GC.sans,
        fontSize: 11,
        color: GC.textDim,
      ),
    ),
  );
}

/// Which way a [GitButton] leans.
enum GitButtonTone { normal, primary, danger }

/// A labeled pill button: icon and text on a raised surface, readable at a
/// glance. Source Control's actions use it instead of bare icons.
class GitButton extends StatelessWidget {
  const GitButton({
    super.key,
    required this.label,
    required this.icon,
    required this.onPressed,
    this.tooltip,
    this.tone = GitButtonTone.normal,
    this.dense = false,
  });

  final String label;
  final IconData icon;
  final VoidCallback? onPressed;
  final String? tooltip;
  final GitButtonTone tone;

  /// Shorter, for hunk headers and section rows.
  final bool dense;

  @override
  Widget build(BuildContext context) {
    final fg = switch (tone) {
      GitButtonTone.normal => GC.textHi,
      GitButtonTone.primary => GC.accentText,
      GitButtonTone.danger => GC.downText,
    };
    final tint = switch (tone) {
      GitButtonTone.normal => GC.textHi,
      GitButtonTone.primary => GC.accent,
      GitButtonTone.danger => GC.down,
    };
    final button = OutlinedButton.icon(
      onPressed: onPressed,
      icon: Icon(icon, size: dense ? 15 : 16),
      label: Text(label),
      style: ButtonStyle(
        minimumSize: WidgetStatePropertyAll(Size(0, dense ? 28 : 32)),
        padding: WidgetStatePropertyAll(
          EdgeInsets.symmetric(horizontal: dense ? 10 : 12),
        ),
        // Standard density: compact would shave the pill below its
        // minimum height and squash the label.
        visualDensity: VisualDensity.standard,
        tapTargetSize: MaterialTapTargetSize.shrinkWrap,
        textStyle: WidgetStatePropertyAll(
          TextStyle(
            fontFamily: GC.sans,
            fontSize: dense ? 12.5 : 13,
            fontWeight: FontWeight.w600,
          ),
        ),
        foregroundColor: WidgetStateProperty.resolveWith(
          (states) => states.contains(WidgetState.disabled) ? GC.textFaint : fg,
        ),
        iconColor: WidgetStateProperty.resolveWith(
          (states) => states.contains(WidgetState.disabled) ? GC.textFaint : fg,
        ),
        backgroundColor: WidgetStateProperty.resolveWith((states) {
          if (states.contains(WidgetState.disabled)) return Colors.transparent;
          final hovered =
              states.contains(WidgetState.hovered) ||
              states.contains(WidgetState.pressed);
          return tint.withValues(alpha: hovered ? 0.18 : 0.08);
        }),
        side: WidgetStateProperty.resolveWith(
          (states) => BorderSide(
            color: states.contains(WidgetState.disabled)
                ? GC.border
                : tint.withValues(
                    alpha: states.contains(WidgetState.hovered) ? 0.55 : 0.3,
                  ),
          ),
        ),
      ),
    );
    return tooltip == null ? button : Tooltip(message: tooltip!, child: button);
  }
}

/// An icon button for dense rows: a readable glyph with a hover plate.
class GitIconButton extends StatelessWidget {
  const GitIconButton({
    super.key,
    required this.tooltip,
    required this.icon,
    required this.onPressed,
    this.color,
    this.size = 30,
  });

  final String tooltip;
  final IconData icon;
  final VoidCallback? onPressed;
  final Color? color;
  final double size;

  @override
  Widget build(BuildContext context) => IconButton(
    tooltip: tooltip,
    onPressed: onPressed,
    icon: Icon(icon, size: 18),
    constraints: BoxConstraints.tightFor(width: size, height: size),
    padding: EdgeInsets.zero,
    style: ButtonStyle(
      shape: WidgetStatePropertyAll(
        RoundedRectangleBorder(borderRadius: BorderRadius.circular(7)),
      ),
      foregroundColor: WidgetStateProperty.resolveWith((states) {
        if (states.contains(WidgetState.disabled)) return GC.textFaint;
        if (color != null) return color;
        return states.contains(WidgetState.hovered) ? GC.textHi : GC.textBody;
      }),
      backgroundColor: WidgetStateProperty.resolveWith(
        (states) => states.contains(WidgetState.hovered)
            ? GC.surface3
            : Colors.transparent,
      ),
      overlayColor: const WidgetStatePropertyAll(Color(0x14FFFFFF)),
    ),
  );
}

/// A confirmation dialog for destructive actions.
Future<bool> confirmDanger(
  BuildContext context, {
  required String title,
  required String message,
  required String action,
}) async {
  final r = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: Text(title),
      content: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 440),
        child: Text(message),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(ctx, false),
          child: const Text('Cancel'),
        ),
        FilledButton(
          style: FilledButton.styleFrom(
            backgroundColor: GC.down,
            foregroundColor: GC.textHi,
          ),
          onPressed: () => Navigator.pop(ctx, true),
          child: Text(action),
        ),
      ],
    ),
  );
  return r ?? false;
}

/// Asks for a single line of text (branch/tag name, stash message…).
Future<String?> promptText(
  BuildContext context, {
  required String title,
  String hint = '',
  String initial = '',
  String action = 'OK',
}) => showDialog<String>(
  context: context,
  builder: (_) =>
      _PromptDialog(title: title, hint: hint, initial: initial, action: action),
);

/// Owns its controller: the dialog keeps rendering the field through its
/// exit animation, after showDialog's future has already completed, so the
/// controller may only be disposed when the dialog itself is.
class _PromptDialog extends StatefulWidget {
  const _PromptDialog({
    required this.title,
    required this.hint,
    required this.initial,
    required this.action,
  });

  final String title;
  final String hint;
  final String initial;
  final String action;

  @override
  State<_PromptDialog> createState() => _PromptDialogState();
}

class _PromptDialogState extends State<_PromptDialog> {
  late final _ctl = TextEditingController(text: widget.initial);

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  void _submit() => Navigator.pop(context, _ctl.text.trim());

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text(widget.title),
    content: SizedBox(
      width: 380,
      child: TextField(
        controller: _ctl,
        autofocus: true,
        decoration: InputDecoration(hintText: widget.hint),
        onSubmitted: (_) => _submit(),
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('Cancel'),
      ),
      FilledButton(onPressed: _submit, child: Text(widget.action)),
    ],
  );
}

/// A dismissable outcome banner: git's error output, or a success line.
class GitNoticeBanner extends StatelessWidget {
  const GitNoticeBanner({
    super.key,
    required this.text,
    required this.error,
    this.onDismiss,
  });

  final String text;
  final bool error;
  final VoidCallback? onDismiss;

  @override
  Widget build(BuildContext context) {
    final color = error ? GC.down : GC.ok;
    return Container(
      margin: const EdgeInsets.fromLTRB(8, 4, 8, 4),
      padding: const EdgeInsets.fromLTRB(10, 6, 2, 6),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(GC.rInput),
        border: Border.all(color: color.withValues(alpha: 0.3)),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: 1),
            child: Icon(
              error
                  ? Icons.error_outline_rounded
                  : Icons.check_circle_outline_rounded,
              size: 15,
              color: error ? GC.downText : GC.ok,
            ),
          ),
          const SizedBox(width: 8),
          Expanded(
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxHeight: 140),
              child: SingleChildScrollView(
                child: SelectableText(
                  text,
                  style: const TextStyle(
                    fontFamily: GC.sans,
                    fontSize: 12,
                    height: 1.35,
                    color: GC.textHi,
                  ),
                ),
              ),
            ),
          ),
          if (onDismiss != null)
            GitIconButton(
              tooltip: 'Dismiss',
              icon: Icons.close_rounded,
              onPressed: onDismiss,
            ),
        ],
      ),
    );
  }
}

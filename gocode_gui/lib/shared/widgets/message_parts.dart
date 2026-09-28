import 'package:flutter/material.dart';
import 'package:gpt_markdown/gpt_markdown.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import 'glass.dart';
import 'tool_view.dart';

/// Streams assistant text with a settled-prefix-fast renderer.
///
/// gpt_markdown caches the settled prefix of a streaming reply and rebuilds
/// only the changing tail — the property that makes per-delta rebuilds cheap.
class StreamedMarkdown extends StatelessWidget {
  const StreamedMarkdown({
    super.key,
    required this.text,
    this.isStreaming = false,
    this.onLinkTap,
  });

  final String text;
  final bool isStreaming;
  final void Function(Uri url)? onLinkTap;

  @override
  Widget build(BuildContext context) {
    if (text.isEmpty) {
      return isStreaming ? const _TypingDots() : const SizedBox.shrink();
    }
    return GptMarkdown(
      text,
      style: Theme.of(context).textTheme.bodyLarge
          ?.copyWith(color: const Color(0xE6F4EFE9)),
      isStreaming: isStreaming,
      onLinkTap: (url, title) => onLinkTap?.call(Uri.parse(url)),
    );
  }
}

class _TypingDots extends StatefulWidget {
  const _TypingDots();

  @override
  State<_TypingDots> createState() => _TypingDotsState();
}

class _TypingDotsState extends State<_TypingDots>
    with SingleTickerProviderStateMixin {
  late final AnimationController _controller = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1200),
  )..repeat();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return FadeTransition(
      opacity: _controller,
      child: const Text('▍', style: TextStyle(color: GC.accent)),
    );
  }
}

/// A reasoning block, collapsed by default like the TUI's.
class ReasoningBlock extends StatelessWidget {
  const ReasoningBlock({super.key, required this.text, this.duration});

  final String text;
  final Duration? duration;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final seconds = duration == null
        ? ''
        : ' · ${(duration!.inMilliseconds / 1000).toStringAsFixed(1)}s';
    return _Disclosure(
      leading: const Icon(
        Icons.psychology_outlined,
        size: 13,
        color: GC.textFaint,
      ),
      title: Text('Thinking$seconds', style: _rowMeta),
      body: (_) => SelectableText(
        text,
        style: theme.textTheme.bodySmall?.copyWith(
          fontStyle: FontStyle.italic,
          height: 1.55,
        ),
      ),
    );
  }
}

/// One tool call as a single dense line — status, name, input preview —
/// that expands in place to its input and output.
class ToolCallCard extends StatelessWidget {
  const ToolCallCard({super.key, required this.part});

  final AssistantPart part;

  @override
  Widget build(BuildContext context) {
    final state = part.state;
    final status = state?.status ?? 'pending';
    final Widget statusIcon = switch (status) {
      ToolState.running => const SizedBox.square(
        dimension: 10,
        child: CircularProgressIndicator(strokeWidth: 1.4),
      ),
      ToolState.statusCompleted => const Icon(
        Icons.check_rounded,
        size: 13,
        color: GC.ok,
      ),
      ToolState.statusError => const Icon(
        Icons.close_rounded,
        size: 13,
        color: GC.down,
      ),
      _ => const Icon(
        Icons.radio_button_unchecked_rounded,
        size: 11,
        color: GC.textFaint,
      ),
    };

    final title = state?.title ?? state?.metadata?['description'] as String?;
    final input = state?.input;
    final inputPreview =
        title ??
        toolSummary(part.name, input) ??
        [
          if (input != null)
            for (final entry in input.entries.take(3))
              '${entry.key}: ${_shorten(entry.value)}',
        ].join('  ·  ');

    return _Disclosure(
      tooltip: switch (status) {
        ToolState.running => 'Running',
        ToolState.statusCompleted => 'Done',
        ToolState.statusError => 'Failed',
        _ => 'Pending',
      },
      leading: statusIcon,
      title: Text.rich(
        TextSpan(
          children: [
            TextSpan(
              text: part.name ?? 'tool',
              style: GC.code.copyWith(
                fontSize: 12,
                height: 1.2,
                fontWeight: FontWeight.w600,
                color: status == ToolState.statusError
                    ? GC.downText
                    : GC.textBody,
              ),
            ),
            if (inputPreview.isNotEmpty)
              TextSpan(text: '   $inputPreview', style: _rowMeta),
          ],
        ),
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      trailing: state?.subagentSessionID == null
          ? null
          : const Icon(
              Icons.subdirectory_arrow_right,
              size: 12,
              color: GC.textFaint,
            ),
      body: (_) => Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (state?.error case final err? when err.isNotEmpty) ...[
            ErrorPanel(message: err),
            const SizedBox(height: 8),
          ],
          ToolDetails(
            name: part.name,
            input: input ?? const {},
            output: state?.output,
          ),
        ],
      ),
    );
  }

  String _shorten(Object value) {
    final s = value.toString().replaceAll('\n', ' ');
    return s.length > 40 ? '${s.substring(0, 40)}…' : s;
  }
}

/// Faint secondary text on a disclosure row.
const _rowMeta = TextStyle(
  fontFamily: GC.sans,
  fontSize: 12,
  height: 1.2,
  color: GC.textFaint,
);

/// A 26px clickable line that reveals [body] beneath it, indented behind a
/// hairline guide. Built lazily: [body] runs only while expanded.
class _Disclosure extends StatefulWidget {
  const _Disclosure({
    required this.leading,
    required this.title,
    required this.body,
    this.trailing,
    this.tooltip,
  });

  final Widget leading;
  final Widget title;
  final Widget? trailing;
  final WidgetBuilder body;

  /// Hover text for [leading], e.g. the status it shows.
  final String? tooltip;

  @override
  State<_Disclosure> createState() => _DisclosureState();
}

class _DisclosureState extends State<_Disclosure> {
  bool _expanded = false;
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    Widget leading = SizedBox.square(
      dimension: 14,
      child: Center(child: widget.leading),
    );
    if (widget.tooltip != null) {
      leading = Tooltip(message: widget.tooltip!, child: leading);
    }
    // Sized to its content while collapsed, so several fit on one line of
    // a Wrap; expanded, the body claims the full width.
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 1),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          MouseRegion(
            onEnter: (_) => setState(() => _hovered = true),
            onExit: (_) => setState(() => _hovered = false),
            child: Material(
              type: MaterialType.transparency,
              child: InkWell(
                borderRadius: BorderRadius.circular(6),
                onTap: () => setState(() => _expanded = !_expanded),
                child: SizedBox(
                  height: 26,
                  child: Padding(
                    padding: const EdgeInsets.symmetric(horizontal: 6),
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        leading,
                        const SizedBox(width: 8),
                        Flexible(child: widget.title),
                        if (widget.trailing != null) ...[
                          const SizedBox(width: 6),
                          widget.trailing!,
                        ],
                        const SizedBox(width: 4),
                        AnimatedOpacity(
                          duration: GC.dur,
                          opacity: _hovered || _expanded ? 1 : 0,
                          child: AnimatedRotation(
                            turns: _expanded ? 0.25 : 0,
                            duration: GC.dur,
                            curve: GC.ease,
                            child: const Icon(
                              Icons.chevron_right_rounded,
                              size: 14,
                              color: GC.textFaint,
                            ),
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            ),
          ),
          AnimatedSize(
            duration: GC.dur,
            curve: GC.ease,
            alignment: Alignment.topLeft,
            child: !_expanded
                ? const SizedBox.shrink()
                : Container(
                    width: double.infinity,
                    margin: const EdgeInsets.only(left: 12, top: 2, bottom: 6),
                    padding: const EdgeInsets.only(left: 13, top: 2),
                    decoration: const BoxDecoration(
                      border: Border(left: BorderSide(color: GC.border)),
                    ),
                    child: widget.body(context),
                  ),
          ),
        ],
      ),
    );
  }
}

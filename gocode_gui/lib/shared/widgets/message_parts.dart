import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:gpt_markdown/gpt_markdown.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import 'glass.dart';

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
    return ExpansionTile(
      dense: true,
      tilePadding: EdgeInsets.zero,
      childrenPadding: const EdgeInsets.only(left: 22, bottom: 8),
      visualDensity: VisualDensity.compact,
      title: Row(
        children: [
          const Icon(Icons.psychology_outlined, size: 15, color: GC.textDim),
          const SizedBox(width: 7),
          Text('Thinking$seconds', style: theme.textTheme.bodySmall),
        ],
      ),
      children: [
        Align(
          alignment: Alignment.centerLeft,
          child: SelectableText(
            text,
            style: theme.textTheme.bodySmall?.copyWith(
              fontStyle: FontStyle.italic,
              height: 1.6,
            ),
          ),
        ),
      ],
    );
  }
}

/// One tool call rendered as a collapsible glass card: name, status, input,
/// output.
class ToolCallCard extends StatelessWidget {
  const ToolCallCard({super.key, required this.part});

  final AssistantPart part;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final state = part.state;
    final status = state?.status ?? 'pending';
    final (label, color) = switch (status) {
      ToolState.running => ('running', GC.accentText),
      ToolState.statusCompleted => ('done', GC.ok),
      ToolState.statusError => ('failed', GC.down),
      _ => ('pending', GC.textDim),
    };

    final title = state?.title ?? state?.metadata?['description'] as String?;
    final input = state?.input;
    final inputPreview = <String>[
      ?title,
      if (title == null && input != null)
        for (final entry in input.entries.take(3))
          '${entry.key}: ${_shorten(entry.value)}',
    ].join('  ·  ');

    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: GlassSurface(
        blur: false,
        shadow: false,
        radius: GC.rItem,
        child: ExpansionTile(
          dense: true,
          tilePadding: const EdgeInsets.symmetric(horizontal: 14),
          childrenPadding: const EdgeInsets.fromLTRB(14, 0, 14, 14),
          expandedCrossAxisAlignment: CrossAxisAlignment.stretch,
          title: Row(
            children: [
              if (status == ToolState.running)
                const SizedBox.square(
                  dimension: 12,
                  child: CircularProgressIndicator(strokeWidth: 1.6),
                )
              else
                Icon(Icons.terminal_rounded, size: 14, color: color),
              const SizedBox(width: 8),
              Flexible(
                child: Text(
                  part.name ?? 'tool',
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: GC.code.copyWith(
                    fontWeight: FontWeight.w600,
                    color: GC.textHi,
                  ),
                ),
              ),
              const SizedBox(width: 10),
              StatusPill(label: label, color: color),
              if (state?.subagentSessionID != null) ...[
                const SizedBox(width: 8),
                const Icon(
                  Icons.subdirectory_arrow_right,
                  size: 12,
                  color: GC.textDim,
                ),
              ],
            ],
          ),
          subtitle: inputPreview.isEmpty
              ? null
              : Padding(
                  padding: const EdgeInsets.only(top: 2),
                  child: Text(
                    inputPreview,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodySmall,
                  ),
                ),
          children: [
            if (input != null && input.isNotEmpty) ...[
              const Caption('input'),
              const SizedBox(height: 6),
              CodeBlock(
                const JsonEncoder.withIndent('  ').convert(input),
                maxHeight: 240,
              ),
              const SizedBox(height: 10),
            ],
            if (state?.error case final err? when err.isNotEmpty) ...[
              ErrorPanel(message: err),
              const SizedBox(height: 10),
            ],
            if (state?.output case final output? when output.isNotEmpty) ...[
              const Caption('output'),
              const SizedBox(height: 6),
              CodeBlock(output, maxHeight: 280),
            ],
          ],
        ),
      ),
    );
  }

  String _shorten(Object value) {
    final s = value.toString().replaceAll('\n', ' ');
    return s.length > 40 ? '${s.substring(0, 40)}…' : s;
  }
}

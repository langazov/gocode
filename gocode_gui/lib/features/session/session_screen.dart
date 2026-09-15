import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/message_parts.dart';
import '../../shared/widgets/model_picker.dart';
import '../../shared/widgets/session_status.dart';
import '../home/providers.dart';
import 'prompt_history.dart';
import 'timeline.dart';

/// The session screen: streaming timeline, with a floating glass composer
/// that carries the agent and model switchers.
class SessionScreen extends ConsumerStatefulWidget {
  const SessionScreen({super.key, required this.sessionID});

  final String sessionID;

  @override
  ConsumerState<SessionScreen> createState() => _SessionScreenState();
}

class _SessionScreenState extends ConsumerState<SessionScreen> {
  final _scroll = ScrollController();
  final _input = TextEditingController();
  final _inputFocus = FocusNode();

  /// False while the route's enter transition runs. The timeline (markdown,
  /// tool cards) and the glass composer are the expensive part of this
  /// screen, so they join the tree only once the page has stopped moving,
  /// instead of being laid out and rasterized mid-transition.
  bool _routeSettled = false;
  Animation<double>? _routeAnimation;

  SessionController? get _controller =>
      sessionControllerRegistry[widget.sessionID];

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final animation = ModalRoute.of(context)?.animation;
    if (identical(animation, _routeAnimation)) return;
    _routeAnimation?.removeStatusListener(_onRouteStatus);
    _routeAnimation = animation;
    if (animation == null || animation.isCompleted) {
      _routeSettled = true;
    } else {
      animation.addStatusListener(_onRouteStatus);
    }
  }

  void _onRouteStatus(AnimationStatus status) {
    if (status == AnimationStatus.completed && !_routeSettled && mounted) {
      setState(() => _routeSettled = true);
    }
  }

  @override
  void dispose() {
    _routeAnimation?.removeStatusListener(_onRouteStatus);
    _scroll.dispose();
    _input.dispose();
    _inputFocus.dispose();
    super.dispose();
  }

  /// The timeline is reversed, so offset 0 is the newest message.
  void _showLatest() {
    if (!_scroll.hasClients || _scroll.offset == 0) return;
    unawaited(
      _scroll.animateTo(
        0,
        duration: const Duration(milliseconds: 250),
        curve: GC.ease,
      ),
    );
  }

  Future<void> _send() async {
    final text = _input.text.trim();
    final controller = _controller;
    if (text.isEmpty || controller == null) return;
    _input.clear();
    _showLatest();
    await _run(() => controller.prompt(text));
  }

  Future<void> _interrupt() async {
    final controller = _controller;
    if (controller == null) return;
    await _run(controller.interrupt);
  }

  Future<void> _setAgent(String agent) async {
    final controller = _controller;
    if (controller == null) return;
    await _run(() => controller.setAgent(agent));
  }

  Future<void> _chooseModel(List<ModelEntry> models, Session session) async {
    final choice = await showModelPicker(
      context,
      models: models,
      selectedKey: session.model?.key,
    );
    final model = choice?.model;
    final controller = _controller;
    if (model == null || controller == null) return;
    await _run(() => controller.setModel(model.providerID, model.id));
  }

  Future<void> _run(Future<void> Function() action) async {
    try {
      await action();
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final asyncState = ref.watch(sessionStateProvider(widget.sessionID));
    final models = ref.watch(modelsProvider).value ?? const <ModelEntry>[];
    final agents = ref.watch(agentsProvider).value ?? const <Agent>[];
    final state = asyncState.value;
    final theme = Theme.of(context);

    return Scaffold(
      extendBodyBehindAppBar: true,
      extendBody: true,
      appBar: PillHeader(
        leading: const HeaderBackButton(),
        title: state == null
            ? Text('Session', style: theme.textTheme.titleSmall)
            : _HeaderTitle(session: state.session, models: models),
        actions: [
          if (state != null)
            Padding(
              padding: const EdgeInsets.only(right: 10),
              child: LiveStatusPill(
                sessionID: widget.sessionID,
                busy: state.busy,
              ),
            ),
        ],
      ),
      bottomNavigationBar: state == null || !_routeSettled
          ? null
          : _FadeIn(
              child: _Composer(
                controller: _input,
                focusNode: _inputFocus,
                state: state,
                agents: agents,
                modelLabel: _modelName(state.session.model, models),
                onSend: () => unawaited(_send()),
                onInterrupt: () => unawaited(_interrupt()),
                onPickModel: () =>
                    unawaited(_chooseModel(models, state.session)),
                onPickAgent: (agent) => unawaited(_setAgent(agent)),
              ),
            ),
      // Empty until the enter transition ends (see _routeSettled), then the
      // content fades in once. Later data updates keep the same key, so
      // streaming doesn't re-trigger the fade.
      body: AnimatedSwitcher(
        duration: _fadeInDuration,
        switchInCurve: GC.ease,
        child: !_routeSettled
            ? const SizedBox.shrink(key: ValueKey('settling'))
            : asyncState.when(
                loading: () => const Center(
                  key: ValueKey('loading'),
                  child: CircularProgressIndicator(strokeWidth: 2),
                ),
                error: (e, _) => Center(
                  key: const ValueKey('error'),
                  child: Padding(
                    padding: const EdgeInsets.all(24),
                    child: ConstrainedBox(
                      constraints: const BoxConstraints(maxWidth: 560),
                      child: ErrorPanel(
                        message: '$e',
                        onRetry: () => ref.invalidate(
                          sessionStateProvider(widget.sessionID),
                        ),
                      ),
                    ),
                  ),
                ),
                data: (state) => KeyedSubtree(
                  key: const ValueKey('timeline'),
                  // Built inside the Scaffold, so the insets include the
                  // header and the composer the timeline scrolls under.
                  child: Builder(
                    builder: (context) => _timeline(context, state),
                  ),
                ),
              ),
      ),
    );
  }

  Widget _timeline(BuildContext context, SessionState state) {
    // Scaffold folds the header and composer heights into this padding.
    final pad = MediaQuery.paddingOf(context);
    final padding = EdgeInsets.fromLTRB(16, pad.top + 12, 16, pad.bottom + 24);
    if (state.items.isEmpty) {
      return SingleChildScrollView(
        padding: padding,
        child: _column(_EmptyTimeline(session: state.session)),
      );
    }
    final items = state.items;
    // Reversed, so offset 0 is the newest message: a session opens at its end
    // with nothing to scroll, only the messages on screen are built, and
    // streamed text stays pinned to the bottom while the reader is there.
    // (Scrolling to the end of a forward list instead lays out and paints
    // every message on the way down.)
    return ListView.builder(
      controller: _scroll,
      reverse: true,
      padding: padding,
      itemCount: items.length,
      itemBuilder: (context, i) =>
          _column(_item(context, state, items[items.length - 1 - i])),
    );
  }

  /// Centers content in an 860px reading column.
  Widget _column(Widget child) => Align(
    alignment: Alignment.topCenter,
    child: ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 860),
      child: SizedBox(width: double.infinity, child: child),
    ),
  );

  Widget _item(BuildContext context, SessionState state, TimelineItem item) {
    final theme = Theme.of(context);
    switch (item) {
      case UserBubble():
        return Align(
          alignment: Alignment.centerRight,
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 640),
            child: Padding(
              padding: const EdgeInsets.only(left: 48, bottom: 18),
              child: GlassSurface(
                blur: false,
                shadow: false,
                radius: GC.rCard,
                tint: const Color(0x1FE8862D),
                borderColor: GC.borderAccent,
                padding: const EdgeInsets.symmetric(
                  horizontal: 16,
                  vertical: 12,
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.end,
                  children: [
                    if (item.text.isNotEmpty)
                      SelectableText(
                        item.text,
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: GC.textHi,
                        ),
                      ),
                    for (final f in item.files)
                      if (f.name != null)
                        Padding(
                          padding: const EdgeInsets.only(top: 4),
                          child: Row(
                            mainAxisSize: MainAxisSize.min,
                            children: [
                              const Icon(
                                Icons.attach_file,
                                size: 12,
                                color: GC.textDim,
                              ),
                              const SizedBox(width: 4),
                              Text(f.name!, style: theme.textTheme.labelSmall),
                            ],
                          ),
                        ),
                  ],
                ),
              ),
            ),
          ),
        );
      case AssistantTurn():
        return _AssistantTurnView(
          turn: item,
          busy: state.busy,
          onOpenSession: (id) => context.push('/session/$id'),
        );
      case NoticeItem():
        return Padding(
          padding: const EdgeInsets.only(bottom: 14),
          child: Row(
            children: [
              const Expanded(child: Divider()),
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 10),
                child: Text(item.text, style: GC.caption),
              ),
              const Expanded(child: Divider()),
            ],
          ),
        );
    }
  }
}

const _fadeInDuration = Duration(milliseconds: 220);

/// Fades its child in once, on first build; later rebuilds don't repeat it.
class _FadeIn extends StatelessWidget {
  const _FadeIn({required this.child});

  final Widget child;

  @override
  Widget build(BuildContext context) => TweenAnimationBuilder<double>(
    tween: Tween(begin: 0, end: 1),
    duration: _fadeInDuration,
    curve: GC.ease,
    builder: (context, opacity, child) =>
        Opacity(opacity: opacity, child: child),
    child: child,
  );
}

String _modelName(ModelRef? ref, List<ModelEntry> models) {
  if (ref == null) return 'default model';
  for (final m in models) {
    if (m.key == ref.key) return m.name;
  }
  return ref.id;
}

class _HeaderTitle extends StatelessWidget {
  const _HeaderTitle({required this.session, required this.models});

  final Session session;
  final List<ModelEntry> models;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          session.title.isEmpty ? 'Untitled' : session.title,
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: theme.textTheme.titleSmall,
        ),
        Text(
          '${session.agent ?? 'build'} · ${_modelName(session.model, models)}',
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: theme.textTheme.bodySmall,
        ),
      ],
    );
  }
}

class _EmptyTimeline extends StatelessWidget {
  const _EmptyTimeline({required this.session});

  final Session session;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: 96),
      child: Column(
        children: [
          const Eyebrow('Ready'),
          const SizedBox(height: 22),
          Text(
            'What are we building?',
            textAlign: TextAlign.center,
            style: theme.textTheme.headlineSmall,
          ),
          const SizedBox(height: 8),
          Text(
            session.directory,
            textAlign: TextAlign.center,
            style: GC.code.copyWith(color: GC.textFaint),
          ),
        ],
      ),
    );
  }
}

class _AssistantTurnView extends StatelessWidget {
  const _AssistantTurnView({
    required this.turn,
    required this.busy,
    this.onOpenSession,
  });

  final AssistantTurn turn;
  final bool busy;
  final void Function(String sessionID)? onOpenSession;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final streaming = busy && turn.error == null && turn.finish == null;
    final who = [
      if (turn.agent.isNotEmpty) turn.agent else 'assistant',
      if (turn.model.id.isNotEmpty) turn.model.id,
    ].join(' · ');

    return Padding(
      padding: const EdgeInsets.only(bottom: 22),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(bottom: 6),
            child: Row(
              children: [
                Container(
                  width: 6,
                  height: 6,
                  decoration: const BoxDecoration(
                    color: GC.accent,
                    shape: BoxShape.circle,
                  ),
                ),
                const SizedBox(width: 8),
                Flexible(
                  child: Text(
                    who,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.labelSmall?.copyWith(
                      color: GC.textFaint,
                    ),
                  ),
                ),
              ],
            ),
          ),
          for (final part in turn.parts)
            switch (part.type) {
              AssistantPart.textType => StreamedMarkdown(
                text: turn.textFor(part),
                isStreaming: streaming,
              ),
              AssistantPart.reasoningType => ReasoningBlock(
                text: turn.textFor(part),
                duration: part.time?.duration,
              ),
              AssistantPart.toolType => ToolCallCard(part: part),
              _ => Text(turn.textFor(part), style: theme.textTheme.bodySmall),
            },
          if (streaming && turn.parts.isEmpty)
            const StreamedMarkdown(text: '', isStreaming: true),
          if (turn.error != null)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: turn.error!.isAborted
                  ? Text('· interrupted', style: theme.textTheme.bodySmall)
                  : ErrorPanel(message: turn.error!.message ?? 'error'),
            ),
          if (turn.tokens != null)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: Text(
                '${turn.tokens!.total} tokens'
                '${turn.cost != null ? ' · \$${turn.cost!.toStringAsFixed(4)}' : ''}',
                style: GC.code.copyWith(fontSize: 11, color: GC.textFaint),
              ),
            ),
        ],
      ),
    );
  }
}

class _Composer extends StatefulWidget {
  const _Composer({
    required this.controller,
    required this.focusNode,
    required this.state,
    required this.agents,
    required this.modelLabel,
    required this.onSend,
    required this.onInterrupt,
    required this.onPickModel,
    required this.onPickAgent,
  });

  final TextEditingController controller;
  final FocusNode focusNode;
  final SessionState state;
  final List<Agent> agents;
  final String modelLabel;
  final VoidCallback onSend;
  final VoidCallback onInterrupt;
  final VoidCallback onPickModel;
  final ValueChanged<String> onPickAgent;

  @override
  State<_Composer> createState() => _ComposerState();
}

/// Recalls previously sent prompts with ↑/↓, like shell history. History is
/// read from the session's own user messages, so it needs no separate store
/// and survives navigating away and back.
class _ComposerState extends State<_Composer> {
  final _history = PromptHistoryCursor();
  String? _lastRecalled;

  @override
  void initState() {
    super.initState();
    widget.controller.addListener(_onTextChanged);
  }

  @override
  void dispose() {
    widget.controller.removeListener(_onTextChanged);
    super.dispose();
  }

  /// A manual edit (including `_send`'s clear) abandons history browsing, so
  /// the next ↑ starts a fresh recall from the top instead of jumping again.
  void _onTextChanged() =>
      _history.noteEdit(widget.controller.text, _lastRecalled);

  List<String> get _prompts => [
    for (final item in widget.state.items.reversed)
      if (item is UserBubble && item.text.trim().isNotEmpty) item.text,
  ];

  void _apply(String text) {
    _lastRecalled = text;
    widget.controller.value = TextEditingValue(
      text: text,
      selection: TextSelection.collapsed(offset: text.length),
    );
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent && event is! KeyRepeatEvent) {
      return KeyEventResult.ignored;
    }
    final selection = widget.controller.selection;
    final String? text;
    if (event.logicalKey == LogicalKeyboardKey.arrowUp) {
      text = _history.older(
        _prompts,
        atStart: selection.isCollapsed && selection.baseOffset <= 0,
        currentText: widget.controller.text,
      );
    } else if (event.logicalKey == LogicalKeyboardKey.arrowDown) {
      text = _history.newer(
        _prompts,
        atEnd:
            selection.isCollapsed &&
            selection.baseOffset >= widget.controller.text.length,
      );
    } else {
      return KeyEventResult.ignored;
    }
    if (text == null) return KeyEventResult.ignored;
    _apply(text);
    return KeyEventResult.handled;
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final busy = widget.state.busy;
    final primary = widget.agents
        .where((a) => !a.hidden && a.mode != 'subagent')
        .toList();
    final currentAgent = widget.state.session.agent ?? 'build';

    return SafeArea(
      top: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
        child: Align(
          alignment: Alignment.bottomCenter,
          heightFactor: 1,
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 860),
            child: GlassSurface(
              radius: GC.rPanel,
              tint: const Color(0x66171717),
              padding: const EdgeInsets.fromLTRB(20, 8, 10, 10),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  if (widget.state.todos.isNotEmpty)
                    _TodoLine(todos: widget.state.todos),
                  if (widget.state.queued.isNotEmpty)
                    Padding(
                      padding: const EdgeInsets.only(top: 6),
                      child: Text(
                        '${widget.state.queued.length} queued — sent when this turn ends',
                        style: theme.textTheme.bodySmall,
                      ),
                    ),
                  // Enter sends, Shift+Enter falls through as a newline; ↑/↓
                  // recall prompt history (see _onKey) and otherwise fall
                  // through to normal cursor movement.
                  Focus(
                    onKeyEvent: _onKey,
                    child: CallbackShortcuts(
                      bindings: {
                        const SingleActivator(LogicalKeyboardKey.enter):
                            widget.onSend,
                        const SingleActivator(LogicalKeyboardKey.numpadEnter):
                            widget.onSend,
                      },
                      child: TextField(
                        controller: widget.controller,
                        focusNode: widget.focusNode,
                        autofocus: true,
                        minLines: 1,
                        maxLines: 8,
                        style: theme.textTheme.bodyLarge?.copyWith(
                          color: GC.textHi,
                        ),
                        decoration: InputDecoration(
                          hintText: busy
                              ? 'Queue a follow-up…'
                              : 'Ask gocode to build, fix, or explain…',
                          filled: false,
                          border: InputBorder.none,
                          enabledBorder: InputBorder.none,
                          focusedBorder: InputBorder.none,
                          contentPadding: const EdgeInsets.symmetric(
                            vertical: 12,
                          ),
                        ),
                      ),
                    ),
                  ),
                  const SizedBox(height: 4),
                  Row(
                    children: [
                      Expanded(
                        child: Wrap(
                          spacing: 8,
                          runSpacing: 8,
                          children: [
                            PopupMenuButton<String>(
                              tooltip: 'Switch agent',
                              onSelected: widget.onPickAgent,
                              itemBuilder: (_) => [
                                for (final a in primary)
                                  PopupMenuItem(
                                    value: a.id,
                                    child: _AgentOption(
                                      agent: a,
                                      selected: a.id == currentAgent,
                                    ),
                                  ),
                              ],
                              child: _Chip(
                                icon: Icons.smart_toy_outlined,
                                label: currentAgent,
                              ),
                            ),
                            _Chip(
                              icon: Icons.memory_rounded,
                              label: widget.modelLabel,
                              onTap: widget.onPickModel,
                            ),
                          ],
                        ),
                      ),
                      if (busy) ...[
                        _RoundButton(
                          icon: Icons.stop_rounded,
                          tooltip: 'Interrupt',
                          onPressed: widget.onInterrupt,
                          danger: true,
                        ),
                        const SizedBox(width: 8),
                      ],
                      ValueListenableBuilder<TextEditingValue>(
                        valueListenable: widget.controller,
                        builder: (context, value, _) => _RoundButton(
                          icon: Icons.arrow_upward_rounded,
                          tooltip: busy ? 'Queue' : 'Send',
                          onPressed: value.text.trim().isEmpty
                              ? null
                              : widget.onSend,
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _Chip extends StatelessWidget {
  const _Chip({required this.icon, required this.label, this.onTap});

  final IconData icon;
  final String label;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final body = Container(
      height: 32,
      padding: const EdgeInsets.symmetric(horizontal: 12),
      decoration: BoxDecoration(
        color: const Color(0x0FFFFFFF),
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: GC.borderStrong),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 14, color: GC.accentText),
          const SizedBox(width: 6),
          ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 220),
            child: Text(
              label,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.labelMedium,
            ),
          ),
          const SizedBox(width: 2),
          const Icon(Icons.expand_more_rounded, size: 16, color: GC.textDim),
        ],
      ),
    );
    if (onTap == null) return body;
    return Material(
      type: MaterialType.transparency,
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(999),
        child: body,
      ),
    );
  }
}

class _AgentOption extends StatelessWidget {
  const _AgentOption({required this.agent, required this.selected});

  final Agent agent;
  final bool selected;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Row(
      children: [
        SizedBox(
          width: 22,
          child: selected
              ? const Icon(Icons.check_rounded, size: 16, color: GC.accent)
              : null,
        ),
        const SizedBox(width: 6),
        ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 260),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(agent.id, style: theme.textTheme.titleSmall),
              if (agent.description != null)
                Text(
                  agent.description!,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodySmall,
                ),
            ],
          ),
        ),
      ],
    );
  }
}

class _RoundButton extends StatelessWidget {
  const _RoundButton({
    required this.icon,
    required this.tooltip,
    required this.onPressed,
    this.danger = false,
  });

  final IconData icon;
  final String tooltip;
  final VoidCallback? onPressed;
  final bool danger;

  @override
  Widget build(BuildContext context) {
    final enabled = onPressed != null;
    final background = danger
        ? GC.down.withValues(alpha: 0.14)
        : enabled
        ? GC.accent
        : GC.accent.withValues(alpha: 0.25);
    final foreground = danger
        ? GC.downText
        : enabled
        ? GC.accentInk
        : GC.accentInk.withValues(alpha: 0.6);
    return Tooltip(
      message: tooltip,
      child: Material(
        color: background,
        shape: CircleBorder(
          side: danger
              ? BorderSide(color: GC.down.withValues(alpha: 0.5))
              : BorderSide.none,
        ),
        child: InkWell(
          customBorder: const CircleBorder(),
          onTap: onPressed,
          child: SizedBox.square(
            dimension: 40,
            child: Icon(icon, size: 20, color: foreground),
          ),
        ),
      ),
    );
  }
}

class _TodoLine extends StatelessWidget {
  const _TodoLine({required this.todos});

  final List<Todo> todos;

  @override
  Widget build(BuildContext context) {
    final done = todos.where((t) => t.isCompleted).length;
    final next = todos
        .where((t) => !t.isCompleted)
        .take(1)
        .map((t) => t.isInProgress ? '▸ ${t.content}' : t.content)
        .join();
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Row(
        children: [
          const Icon(Icons.checklist_rounded, size: 14, color: GC.textDim),
          const SizedBox(width: 8),
          Text(
            '$done/${todos.length}',
            style: GC.code.copyWith(fontSize: 11.5),
          ),
          const SizedBox(width: 10),
          Expanded(
            child: Text(
              next,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ),
        ],
      ),
    );
  }
}

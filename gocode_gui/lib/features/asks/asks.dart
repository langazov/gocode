import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/acp/client.dart';
import '../../core/acp/connection.dart';
import '../../core/api/client.dart';
import '../../core/api/models.dart';
import '../../core/connection/controller.dart';
import '../session/timeline.dart' show acpConnectionProvider;
import '../../shared/widgets/diff_view.dart';
import '../../shared/widgets/glass.dart';

/// Pending permission asks and questions. In HTTP mode these are polled and
/// reconciled from events; in ACP mode the agent itself pushes each ask as
/// a server→client request that stays open until answered here.
class AsksNotifier extends Notifier<List<Ask>> {
  Timer? _poll;
  StreamSubscription<ApiEvent>? _sub;
  StreamSubscription<AcpPermissionRequest>? _acpSub;
  ProviderSubscription<ConnectionPhase>? _acpModeSub;

  @override
  List<Ask> build() {
    ref.onDispose(() {
      _poll?.cancel();
      _sub?.cancel();
      _acpSub?.cancel();
      _acpModeSub?.close();
    });

    final acp = ref.watch(acpConnectionProvider);
    if (acp != null) {
      // ACP asks arrive as requests that block the agent's turn; the list
      // here mirrors the pending set 1:1.
      _acpSub = acp.permissionRequests.listen((request) {
        state = [...state, AcpPermissionAsk(request)];
      });
      // Leaving ACP mode clears any unanswered asks with the connection.
      _acpModeSub = ref.listen(
        connectionProvider.select((s) => s.phase),
        (_, _) {
          if (ref.read(acpConnectionProvider) == null) {
            state = state.where((a) => a is! AcpPermissionAsk).toList();
          }
        },
      );
      return state;
    }

    final client = ref.watch(apiClientProvider);
    final connection = ref.watch(connectionControllerProvider);
    if (client == null) return const [];

    // Events hint that asks may have changed; reconcile both lists.
    _sub = connection?.events.listen((event) {
      switch (event.type) {
        case 'permission.updated':
        case 'permission.request':
        case 'question.updated':
        case 'question.request':
          _refresh(client);
      }
    });

    // Poll as a safety net: a dropped event must not leave a stuck dialog.
    _poll = Timer.periodic(const Duration(seconds: 2), (_) {
      _refresh(client);
    });
    unawaited(_refresh(client));
    return const [];
  }

  Future<void> _refresh(GocodeClient client) async {
    try {
      final results = await Future.wait<dynamic>([
        client.permissionRequests(),
        client.questions(),
      ]);
      state = [
        ...(results[0] as List<PermissionRequest>).map(PermissionAsk.new),
        ...(results[1] as List<QuestionRequest>).map(QuestionAsk.new),
      ];
    } catch (_) {
      // Transient; the next tick retries.
    }
  }

  void _dropAcpAskById(String id) {
    state = state.where((a) => a.id != id).toList();
  }

  Future<void> replyPermission(
    PermissionRequest request,
    String reply, {
    String? message,
  }) async {
    final client = ref.read(apiClientProvider);
    if (client == null) return;
    try {
      await client.replyPermission(
        request.sessionID,
        request.id,
        reply,
        message: message,
      );
    } finally {
      await _refresh(client);
    }
  }

  Future<void> replyQuestion(
    QuestionRequest request,
    List<List<String>> answers,
  ) async {
    final client = ref.read(apiClientProvider);
    if (client == null) return;
    try {
      await client.replyQuestion(request.id, answers);
    } finally {
      await _refresh(client);
    }
  }

  Future<void> rejectQuestion(QuestionRequest request) async {
    final client = ref.read(apiClientProvider);
    if (client == null) return;
    try {
      await client.rejectQuestion(request.id);
    } finally {
      await _refresh(client);
    }
  }
}

final asksProvider = NotifierProvider<AsksNotifier, List<Ask>>(
  AsksNotifier.new,
);

sealed class Ask {
  const Ask();

  String get id;
  String get sessionID;
}

/// One ACP permission ask. [optionIds]/[optionLabels] expose the options
/// the agent offered; answering goes through the ACP connection.
class AcpPermissionAsk extends Ask {
  const AcpPermissionAsk(this.request);

  final AcpPermissionRequest request;

  @override
  String get id => 'acp_${request.hashCode}';
  @override
  String get sessionID => request.sessionID;

  List<String> get optionIds => [for (final o in request.options) o.id];
  List<String> get optionLabels => [for (final o in request.options) o.name];

  String get title => request.title;
  String? get diffText => request.diffText;

  bool get canAlways => optionIds.contains('always');

  Future<void> reply(AcpConnection acp, String optionId) async {
    await acp.replyPermission(request, optionId);
  }
}

class PermissionAsk extends Ask {
  const PermissionAsk(this.request);

  final PermissionRequest request;

  @override
  String get id => request.id;
  @override
  String get sessionID => request.sessionID;
}

class QuestionAsk extends Ask {
  const QuestionAsk(this.request);

  final QuestionRequest request;

  @override
  String get id => request.id;
  @override
  String get sessionID => request.sessionID;
}

/// Shows pending asks as sheets over whatever screen is up, one at a time
/// (the first pending ask wins).
///
/// Lives in MaterialApp's builder — above the router's Navigator — so it
/// pushes on [navigatorKey] instead of looking a navigator up by context.
class AsksOverlay extends ConsumerStatefulWidget {
  const AsksOverlay({
    super.key,
    required this.navigatorKey,
    required this.child,
  });

  final GlobalKey<NavigatorState> navigatorKey;
  final Widget child;

  @override
  ConsumerState<AsksOverlay> createState() => _AsksOverlayState();
}

class _AsksOverlayState extends ConsumerState<AsksOverlay> {
  Ask? _shown;
  Route<void>? _route;

  void _sync(List<Ask> asks) {
    final navigator = widget.navigatorKey.currentState;
    if (navigator == null) return;
    final next = asks.isEmpty ? null : asks.first;
    if (next?.id == _shown?.id) return;

    // Remove our own sheet specifically, never whatever route is on top.
    final previous = _route;
    _route = null;
    _shown = null;
    if (previous != null && previous.isActive) navigator.removeRoute(previous);
    if (next == null) return;

    final route = ModalBottomSheetRoute<void>(
      builder: (_) => _AskSheet(ask: next),
      isScrollControlled: true,
      isDismissible: false,
      enableDrag: false,
      backgroundColor: Colors.transparent,
      elevation: 0,
      modalBarrierColor: const Color(0x8C000000),
    );
    _shown = next;
    _route = route;
    unawaited(
      navigator.push(route).whenComplete(() {
        if (identical(_route, route)) {
          _route = null;
          _shown = null;
        }
      }),
    );
  }

  @override
  Widget build(BuildContext context) {
    ref.listen(asksProvider, (_, next) => _sync(next));
    return widget.child;
  }
}

class _AskSheet extends ConsumerWidget {
  const _AskSheet({required this.ask});

  final Ask ask;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
        child: ConstrainedBox(
          constraints: BoxConstraints(
            maxHeight: MediaQuery.sizeOf(context).height * 0.8,
          ),
          child: GlassSurface(
            radius: GC.rPanel,
            tint: GC.surface1.withValues(alpha: 0.8),
            padding: const EdgeInsets.all(22),
            child: switch (ask) {
              PermissionAsk(:final request) => _PermissionSheet(
                request: request,
              ),
              QuestionAsk(:final request) => _QuestionSheet(request: request),
              final AcpPermissionAsk acpAsk => _AcpPermissionSheet(ask: acpAsk),
            },
          ),
        ),
      ),
    );
  }
}

final _dangerStyle = OutlinedButton.styleFrom(
  foregroundColor: GC.downText,
  iconColor: GC.downText,
  backgroundColor: Colors.transparent,
  side: BorderSide(color: GC.down.withValues(alpha: 0.45)),
);

class _PermissionSheet extends ConsumerWidget {
  const _PermissionSheet({required this.request});

  final PermissionRequest request;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final notifier = ref.read(asksProvider.notifier);
    final isEdit = request.metadata['diff'] is String;
    final busy = ref.watch(_replyingProvider);

    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Container(
              width: 36,
              height: 36,
              decoration: BoxDecoration(
                color: GC.accent.withValues(alpha: 0.14),
                borderRadius: BorderRadius.circular(GC.rInput),
                border: Border.all(color: GC.borderAccent),
              ),
              child: const Icon(
                Icons.shield_outlined,
                size: 18,
                color: GC.accentText,
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Caption('Permission'),
                  Text(
                    '${request.agent ?? 'agent'} wants to ${request.action}',
                    style: theme.textTheme.titleMedium,
                  ),
                ],
              ),
            ),
          ],
        ),
        const SizedBox(height: 16),
        Flexible(
          child: SingleChildScrollView(
            child: isEdit
                ? DiffView(patch: request.detail)
                : CodeBlock(request.detail),
          ),
        ),
        const SizedBox(height: 18),
        if (busy)
          const Center(
            child: SizedBox.square(
              dimension: 22,
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
          )
        else
          Wrap(
            alignment: WrapAlignment.end,
            spacing: 8,
            runSpacing: 8,
            children: [
              OutlinedButton.icon(
                style: _dangerStyle,
                onPressed: () => _reply(ref, notifier, 'reject'),
                icon: const Icon(Icons.block, size: 18),
                label: const Text('Deny'),
              ),
              if (request.save.isNotEmpty)
                OutlinedButton.icon(
                  onPressed: () => _reply(ref, notifier, 'always'),
                  icon: const Icon(Icons.done_all_rounded, size: 18),
                  label: const Text('Always allow'),
                ),
              FilledButton.icon(
                onPressed: () => _reply(ref, notifier, 'once'),
                icon: const Icon(Icons.check_rounded, size: 18),
                label: const Text('Allow once'),
              ),
            ],
          ),
      ],
    );
  }

  Future<void> _reply(
    WidgetRef ref,
    AsksNotifier notifier,
    String reply,
  ) async {
    final container = ProviderScope.containerOf(ref.context, listen: false);
    container.read(_replyingProvider.notifier).set(true);
    try {
      await notifier.replyPermission(request, reply);
    } finally {
      container.read(_replyingProvider.notifier).set(false);
    }
  }
}

final _replyingProvider = NotifierProvider<_ReplyingNotifier, bool>(
  _ReplyingNotifier.new,
);

class _ReplyingNotifier extends Notifier<bool> {
  @override
  bool build() => false;

  void set(bool value) => state = value;
}

/// The ACP permission sheet: the ask's own title and options, rendered
/// like the HTTP one.
class _AcpPermissionSheet extends ConsumerWidget {
  const _AcpPermissionSheet({required this.ask});

  final AcpPermissionAsk ask;

  Future<void> _reply(WidgetRef ref, String optionId) async {
    final container = ProviderScope.containerOf(ref.context, listen: false);
    container.read(_replyingProvider.notifier).set(true);
    try {
      final acp = container.read(acpConnectionProvider);
      await ask.reply(acp!, optionId);
      // The pending list drops this ask once its request resolves.
      container
          .read(asksProvider.notifier)
          ._dropAcpAskById(ask.id);
    } finally {
      container.read(_replyingProvider.notifier).set(false);
    }
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final busy = ref.watch(_replyingProvider);
    final diff = ask.diffText;

    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Container(
              width: 36,
              height: 36,
              decoration: BoxDecoration(
                color: GC.accent.withValues(alpha: 0.14),
                borderRadius: BorderRadius.circular(GC.rInput),
                border: Border.all(color: GC.borderAccent),
              ),
              child: const Icon(
                Icons.shield_outlined,
                size: 18,
                color: GC.accentText,
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Caption('Permission'),
                  Text(ask.title, style: theme.textTheme.titleMedium),
                ],
              ),
            ),
          ],
        ),
        const SizedBox(height: 16),
        if (diff != null)
          Flexible(
            child: SingleChildScrollView(child: DiffView(patch: diff)),
          )
        else ...[
          const SizedBox(height: 4),
        ],
        const SizedBox(height: 18),
        if (busy)
          const Center(
            child: SizedBox.square(
              dimension: 22,
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
          )
        else
          Wrap(
            alignment: WrapAlignment.end,
            spacing: 8,
            runSpacing: 8,
            children: [
              OutlinedButton.icon(
                style: _dangerStyle,
                onPressed: () => unawaited(_reply(ref, 'reject')),
                icon: const Icon(Icons.block, size: 18),
                label: const Text('Deny'),
              ),
              if (ask.canAlways)
                OutlinedButton.icon(
                  onPressed: () => unawaited(_reply(ref, 'always')),
                  icon: const Icon(Icons.done_all_rounded, size: 18),
                  label: const Text('Always allow'),
                ),
              FilledButton.icon(
                onPressed: () => unawaited(_reply(ref, 'once')),
                icon: const Icon(Icons.check_rounded, size: 18),
                label: const Text('Allow once'),
              ),
            ],
          ),
      ],
    );
  }
}

class _QuestionSheet extends ConsumerWidget {
  const _QuestionSheet({required this.request});

  final QuestionRequest request;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final notifier = ref.read(asksProvider.notifier);
    final selections = List<Set<String>>.generate(
      request.questions.length,
      (_) => {},
    );

    return StatefulBuilder(
      builder: (context, setState) => SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            const Caption('Question'),
            const SizedBox(height: 6),
            for (var qi = 0; qi < request.questions.length; qi++) ...[
              Text(
                request.questions[qi].question,
                style: theme.textTheme.titleMedium,
              ),
              if (request.questions[qi].header.isNotEmpty)
                Padding(
                  padding: const EdgeInsets.only(top: 2),
                  child: Text(
                    request.questions[qi].header,
                    style: theme.textTheme.bodySmall,
                  ),
                ),
              const SizedBox(height: 8),
              for (final option in request.questions[qi].options)
                CheckboxListTile(
                  dense: true,
                  contentPadding: EdgeInsets.zero,
                  controlAffinity: ListTileControlAffinity.leading,
                  value: selections[qi].contains(option.label),
                  title: Text(option.label, style: theme.textTheme.titleSmall),
                  subtitle: option.description == null
                      ? null
                      : Text(
                          option.description!,
                          style: theme.textTheme.bodySmall,
                        ),
                  onChanged: (checked) => setState(() {
                    final set = selections[qi];
                    if (request.questions[qi].multiple) {
                      checked!
                          ? set.add(option.label)
                          : set.remove(option.label);
                    } else {
                      set
                        ..clear()
                        ..add(option.label);
                    }
                  }),
                ),
              const SizedBox(height: 12),
            ],
            Wrap(
              alignment: WrapAlignment.end,
              spacing: 8,
              runSpacing: 8,
              children: [
                OutlinedButton(
                  onPressed: () => notifier.rejectQuestion(request),
                  child: const Text('Dismiss'),
                ),
                FilledButton(
                  onPressed: () => notifier.replyQuestion(
                    request,
                    selections.map((s) => s.toList()).toList(),
                  ),
                  child: const Text('Answer'),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

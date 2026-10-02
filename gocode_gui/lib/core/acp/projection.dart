import '../../features/session/timeline.dart';
import '../api/models.dart';
import 'protocol.dart';

/// Projects a stream of ACP session updates onto the GUI's existing
/// [SessionState] timeline model, so the HTTP and ACP transports feed the
/// same widgets.
///
/// ACP is id-based rather than message-row-based: each text/reasoning part
/// and each tool call has its own stable id. gocode derives text and
/// reasoning ids from the assistant message id (`msg-text`,
/// `msg-reasoning`), but tool calls keep the provider's raw call id — so
/// grouping is prefix-based for text and "current turn"-based for tools:
/// a tool call joins the turn of the most recent text/reasoning part,
/// which is how the agent interleaves them (stream order). A user message
/// closes the turn.
class AcpProjection {
  AcpProjection(this.session);

  final Session session;

  /// Ordered ids as they first appeared — the timeline's row order.
  final List<String> _order = <String>[];

  final _turns = <String, AssistantTurn>{};
  final _userMessages = <String, UserBubble>{};
  final _notices = <String, NoticeItem>{};

  /// The last plan (todo list) reported for the session.
  List<Todo> todos = const [];

  /// The v1 stop reason of the last completed turn, if any.
  String? lastStopReason;

  /// Whether any update has been seen (guards "reconcile vs start fresh").
  bool started = false;

  List<TimelineItem> get items {
    final out = <TimelineItem>[];
    for (final id in _order) {
      final user = _userMessages[id];
      if (user != null) {
        out.add(user);
        continue;
      }
      final notice = _notices[id];
      if (notice != null) {
        out.add(notice);
        continue;
      }
      final turn = _turns[id];
      if (turn != null) out.add(turn);
    }
    return out;
  }

  /// The turn new updates join. Set by every text/reasoning part; cleared
  /// by a user message (the natural turn boundary).
  String? _currentTurn;

  /// Extracts the grouping key from an ACP part id: gocode derives text
  /// and reasoning ids from the assistant message id with known suffixes.
  static String turnKeyOf(String id) {
    for (final suffix in const ['-text', '-reasoning', '-error']) {
      if (id.endsWith(suffix)) {
        return id.substring(0, id.length - suffix.length);
      }
    }
    return id;
  }

  /// Applies one update. Returns true when the visible timeline changed.
  bool apply(AcpSessionUpdate update) {
    started = true;
    switch (update.kind) {
      case 'user_message_chunk':
      case 'user_message':
        final existing = _userMessages[update.messageID];
        if (existing != null) {
          _userMessages[update.messageID] = UserBubble(
            messageID: update.messageID,
            text: existing.text + update.text,
            files: existing.files,
            timeCreated: existing.timeCreated,
          );
        } else {
          _currentTurn = null;
          _order.add(update.messageID);
          _userMessages[update.messageID] = UserBubble(
            messageID: update.messageID,
            text: update.text,
            files: const [],
            timeCreated: DateTime.now().millisecondsSinceEpoch,
          );
        }
        return true;
      case 'agent_message_chunk':
        return _appendChunk(update.messageID, update.text, 'text');
      case 'agent_thought_chunk':
        return _appendChunk(update.messageID, update.text, 'reasoning');
      case 'agent_message':
      case 'agent_thought':
        return _setWhole(
          update.messageID,
          update.contentBlocks
              .map((b) => b['text'] as String? ?? '')
              .toList(),
          update.kind == 'agent_thought' ? 'reasoning' : 'text',
        );
      case 'tool_call':
        _startToolCall(update);
        return true;
      case 'tool_call_update':
        return _updateToolCall(update);
      case 'plan':
        _applyPlan(update);
        return true;
      case 'session_info_update':
        _applySessionInfo(update);
        return true;
      case 'available_commands_update':
      case 'current_mode_update':
      case 'config_option_update':
      case 'usage_update':
        return false;
      default:
        return false;
    }
  }

  bool _appendChunk(String messageID, String delta, String kind) {
    if (delta.isEmpty) return false;
    _currentTurn = turnKeyOf(messageID);
    final turn = _turnFor(messageID);
    turn.applyDelta(messageID, delta);
    if (turn.partByID(messageID) == null) {
      turn.parts.add(AssistantPart(type: kind, id: messageID));
    }
    return true;
  }

  bool _setWhole(String messageID, List<String> texts, String kind) {
    _currentTurn = turnKeyOf(messageID);
    final turn = _turnFor(messageID);
    final text = texts.join('\n');
    // A whole-message upsert replaces whatever was streamed for the id.
    final part = turn.partByID(messageID);
    if (part == null) {
      turn.parts.add(AssistantPart(type: kind, id: messageID, text: text));
    } else {
      final index = turn.parts.indexOf(part);
      turn.parts[index] = AssistantPart(
        type: kind,
        id: messageID,
        text: text,
        time: part.time,
      );
    }
    return true;
  }

  AssistantTurn _turnFor(String id, {String? fallbackTurn}) {
    var key = turnKeyOf(id);
    // A tool call's own key is its raw call id; when a turn is open the
    // call belongs to it instead.
    if (fallbackTurn != null && key == id && _turns.containsKey(fallbackTurn)) {
      key = fallbackTurn;
    }
    var turn = _turns[key];
    if (turn == null) {
      turn = AssistantTurn(
        messageID: key,
        agent: session.agent ?? 'build',
        model: session.model ?? const ModelRef(providerID: '', id: ''),
        timeCreated: DateTime.now().millisecondsSinceEpoch,
      );
      _turns[key] = turn;
      _order.add(key);
    }
    return turn;
  }

  void _startToolCall(AcpSessionUpdate update) {
    final turn = _turnFor(update.toolCallID, fallbackTurn: _currentTurn);
    final raw = update.raw;
    turn.parts.add(
      AssistantPart(
        type: AssistantPart.toolType,
        id: update.toolCallID,
        name: raw['name'] as String?,
        state: ToolState(
          status: 'pending',
          input: _rawInputOf(raw),
          title: raw['title'] as String?,
          metadata: raw['locations'] is List
              ? {'locations': raw['locations']}
              : null,
        ),
      ),
    );
  }

  Map<String, dynamic>? _rawInputOf(Map<String, dynamic> raw) {
    final input = raw['rawInput'];
    return input is Map<String, dynamic> ? input : null;
  }

  bool _updateToolCall(AcpSessionUpdate update) {
    final raw = update.raw;
    final status = raw['status'] as String?;
    final title = raw['title'] as String?;
    var part = _toolPart(update.toolCallID);
    if (part == null) {
      // An update with no preceding tool_call (a replayed settlement):
      // synthesize the part, then apply this update to it.
      _startToolCall(update);
      part = _toolPart(update.toolCallID);
      if (part == null) return false;
    }
    final state = part.state;
    if (status != null) {
      final turn = _toolTurn(update.toolCallID);
      final index = turnPartIndex(update.toolCallID);
      if (turn != null && index != null) {
        turn.parts[index] = AssistantPart(
          type: AssistantPart.toolType,
          id: update.toolCallID,
          name: part.name,
          state: ToolState(
            status: _toolStatus(status),
            input: state?.input,
            error: state?.error,
            output: _outputText(raw, state?.output),
            title: title ?? state?.title,
            metadata: state?.metadata,
            completed: status == 'completed' || status == 'failed'
                ? DateTime.now().millisecondsSinceEpoch
                : state?.completed,
          ),
        );
      }
    }
    return true;
  }

  /// The turn holding a tool call id: the call's own group when it exists
  /// (replay), else the open turn it joined live.
  AssistantTurn? _toolTurn(String callID) {
    final own = _turns[turnKeyOf(callID)];
    if (own != null && own != _turns[_currentTurn]) {
      final found = own.parts.any((p) => p.id == callID && p.isTool);
      if (found) return own;
    }
    final open = _turns[_currentTurn];
    if (open != null && open.parts.any((p) => p.id == callID && p.isTool)) {
      return open;
    }
    return own;
  }

  int? turnPartIndex(String callID) {
    final turn = _toolTurn(callID);
    if (turn == null) return null;
    for (var i = 0; i < turn.parts.length; i++) {
      final part = turn.parts[i];
      if (part.id == callID && part.isTool) return i;
    }
    return null;
  }

  AssistantPart? _toolPart(String callID) {
    final turn = _toolTurn(callID);
    if (turn == null) return null;
    for (final part in turn.parts) {
      if (part.id == callID && part.isTool) return part;
    }
    return null;
  }

  static String _toolStatus(String acpStatus) {
    switch (acpStatus) {
      case 'in_progress':
        return ToolState.running;
      case 'completed':
        return ToolState.statusCompleted;
      case 'failed':
      case 'cancelled':
        return ToolState.statusError;
      default:
        return 'pending';
    }
  }

  String? _outputText(Map<String, dynamic> raw, String? previous) {
    // v1 tool_call_update carries settled output as content blocks.
    final blocks = (raw['content'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>();
    final buffer = StringBuffer();
    for (final block in blocks) {
      switch (block['type']) {
        case 'content':
          final inner = block['content'];
          if (inner is Map<String, dynamic> && inner['text'] is String) {
            buffer.writeln(inner['text'] as String);
          }
        case 'diff':
          final oldText = block['oldText'] as String? ?? '';
          final newText = block['newText'] as String? ?? '';
          buffer.writeln('--- $oldText');
          buffer.writeln('+++ $newText');
      }
    }
    final text = buffer.toString();
    if (text.isEmpty) return previous;
    return previous == null || previous.isEmpty ? text : '$previous\n$text';
  }

  void _applyPlan(AcpSessionUpdate update) {
    final entries = (update.raw['entries'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>();
    todos = [
      for (final entry in entries)
        Todo(
          content: entry['content'] as String? ?? '',
          status: entry['status'] as String? ?? 'pending',
          priority: entry['priority'] as String? ?? 'medium',
          position: todos.length,
        ),
    ];
  }

  void _applySessionInfo(AcpSessionUpdate update) {
    final title = update.raw['title'] as String?;
    if (title != null && title.isNotEmpty) {
      // The Session object is immutable; the caller reads `title` through
      // the projection's own copy.
      _title = title;
    }
  }

  String? _title;
  String? get title => _title ?? session.title;
}

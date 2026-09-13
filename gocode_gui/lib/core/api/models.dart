/// API models mirroring gocode's wire shapes (internal/tui/client/client.go
/// and internal/server/*.go are the canonical references).
library;

class ModelRef {
  const ModelRef({required this.providerID, required this.id, this.variant});

  factory ModelRef.fromJson(Map<String, dynamic> json) => ModelRef(
    providerID: json['providerID'] as String? ?? '',
    id: json['id'] as String? ?? '',
    variant: json['variant'] as String?,
  );

  final String providerID;
  final String id;
  final String? variant;

  /// The canonical `provider/model` key used in pickers and titles.
  String get key => '$providerID/$id';

  Map<String, dynamic> toJson() => {
    'providerID': providerID,
    'id': id,
    if (variant != null && variant!.isNotEmpty) 'variant': variant,
  };

  @override
  String toString() => key;
}

class Session {
  const Session({
    required this.id,
    required this.title,
    required this.directory,
    this.projectID = '',
    this.parentID,
    this.agent,
    this.version = '',
    this.model,
    required this.timeCreated,
    required this.timeUpdated,
  });

  factory Session.fromJson(Map<String, dynamic> json) => Session(
    id: json['id'] as String? ?? '',
    projectID: json['projectID'] as String? ?? '',
    parentID: json['parentID'] as String?,
    agent: json['agent'] as String?,
    title: json['title'] as String? ?? '',
    directory: json['directory'] as String? ?? '',
    version: json['version'] as String? ?? '',
    model: json['model'] == null
        ? null
        : ModelRef.fromJson(json['model'] as Map<String, dynamic>),
    timeCreated: (json['timeCreated'] as num?)?.toInt() ?? 0,
    timeUpdated: (json['timeUpdated'] as num?)?.toInt() ?? 0,
  );

  final String id;
  final String projectID;

  /// Links a subagent/forked session to the one that spawned it.
  final String? parentID;
  final String? agent;
  final String title;
  final String directory;
  final String version;
  final ModelRef? model;
  final int timeCreated;
  final int timeUpdated;

  bool get isSubagent => parentID != null && parentID!.isNotEmpty;

  Map<String, dynamic> toJson() => {
    'id': id,
    'projectID': projectID,
    if (parentID != null) 'parentID': parentID,
    if (agent != null) 'agent': agent,
    'title': title,
    'directory': directory,
    'version': version,
    if (model != null) 'model': model!.toJson(),
    'timeCreated': timeCreated,
    'timeUpdated': timeUpdated,
  };
}

class FileAttachment {
  const FileAttachment({
    required this.mime,
    this.uri,
    this.name,
    this.description,
  });

  factory FileAttachment.fromJson(Map<String, dynamic> json) => FileAttachment(
    uri: json['uri'] as String?,
    mime: json['mime'] as String? ?? '',
    name: json['name'] as String?,
    description: json['description'] as String?,
  );

  final String? uri;
  final String mime;
  final String? name;
  final String? description;

  Map<String, dynamic> toJson() => {
    if (uri != null) 'uri': uri,
    'mime': mime,
    if (name != null) 'name': name,
    if (description != null) 'description': description,
  };
}

/// A message in a session timeline. [data] is a tagged union keyed by [type];
/// decode with [UserData.fromJson] or [AssistantData.fromJson].
class Message {
  const Message({
    required this.id,
    required this.sessionID,
    required this.type,
    required this.seq,
    required this.timeCreated,
    required this.data,
  });

  factory Message.fromJson(Map<String, dynamic> json) => Message(
    id: json['id'] as String? ?? '',
    sessionID: json['sessionID'] as String? ?? '',
    type: json['type'] as String? ?? '',
    seq: (json['seq'] as num?)?.toInt() ?? 0,
    timeCreated: (json['timeCreated'] as num?)?.toInt() ?? 0,
    data: (json['data'] as Map<String, dynamic>?) ?? const {},
  );

  static const user = 'user';
  static const assistant = 'assistant';
  static const synthetic = 'synthetic';
  static const system = 'system';
  static const shell = 'shell';
  static const agentSwitched = 'agent-switched';
  static const modelSwitched = 'model-switched';
  static const compaction = 'compaction';

  final String id;
  final String sessionID;
  final String type;
  final int seq;
  final int timeCreated;
  final Map<String, dynamic> data;

  bool get isUser => type == user;
  bool get isAssistant => type == assistant;
}

class UserData {
  const UserData({required this.text, this.files = const []});

  factory UserData.fromJson(Map<String, dynamic> json) => UserData(
    text: json['text'] as String? ?? '',
    files:
        ((json['files'] as List<dynamic>?)
            ?.whereType<Map<String, dynamic>>()
            .map(FileAttachment.fromJson)
            .toList()) ??
        const [],
  );

  final String text;
  final List<FileAttachment> files;
}

class AssistantError {
  const AssistantError({required this.type, this.message});

  factory AssistantError.fromJson(Map<String, dynamic> json) => AssistantError(
    type: json['type'] as String? ?? 'unknown',
    message: json['message'] as String?,
  );

  /// "aborted" marks a user-ordered interruption; anything else is a failure.
  static const aborted = 'aborted';

  final String type;
  final String? message;

  bool get isAborted => type == aborted;
}

class AssistantTokens {
  const AssistantTokens({
    required this.input,
    required this.output,
    required this.reasoning,
    required this.cacheRead,
    required this.cacheWrite,
  });

  factory AssistantTokens.fromJson(Map<String, dynamic> json) {
    final cache = json['cache'] as Map<String, dynamic>? ?? const {};
    return AssistantTokens(
      input: (json['input'] as num?)?.toInt() ?? 0,
      output: (json['output'] as num?)?.toInt() ?? 0,
      reasoning: (json['reasoning'] as num?)?.toInt() ?? 0,
      cacheRead: (cache['read'] as num?)?.toInt() ?? 0,
      cacheWrite: (cache['write'] as num?)?.toInt() ?? 0,
    );
  }

  final int input;
  final int output;
  final int reasoning;
  final int cacheRead;
  final int cacheWrite;

  int get total => input + output + reasoning + cacheRead + cacheWrite;
}

/// One part of an assistant message: text, reasoning, or a tool call.
class AssistantPart {
  const AssistantPart({
    required this.type,
    required this.id,
    this.text,
    this.name,
    this.state,
    this.time,
  });

  factory AssistantPart.fromJson(Map<String, dynamic> json) => AssistantPart(
    type: json['type'] as String? ?? '',
    id: json['id'] as String? ?? '',
    text: json['text'] as String?,
    name: json['name'] as String?,
    state: json['state'] == null
        ? null
        : ToolState.fromJson(json['state'] as Map<String, dynamic>),
    time: json['time'] == null
        ? null
        : PartTime.fromJson(json['time'] as Map<String, dynamic>),
  );

  static const textType = 'text';
  static const reasoningType = 'reasoning';
  static const toolType = 'tool';

  final String type;
  final String id;
  final String? text;
  final String? name;

  /// Present only for tool parts.
  final ToolState? state;

  /// Present only for text/reasoning parts.
  final PartTime? time;

  bool get isText => type == textType;
  bool get isReasoning => type == reasoningType;
  bool get isTool => type == toolType;
}

class PartTime {
  const PartTime({required this.created, this.completed});

  factory PartTime.fromJson(Map<String, dynamic> json) => PartTime(
    created: (json['created'] as num?)?.toInt() ?? 0,
    completed: (json['completed'] as num?)?.toInt(),
  );

  final int created;
  final int? completed;

  Duration? get duration {
    final done = completed;
    if (done == null || done == 0) return null;
    final d = done - created;
    return d > 0 ? Duration(milliseconds: d) : null;
  }
}

class ToolState {
  const ToolState({
    required this.status,
    this.input,
    this.error,
    this.output,
    this.title,
    this.metadata,
    this.completed,
  });

  factory ToolState.fromJson(Map<String, dynamic> json) => ToolState(
    status: json['status'] as String? ?? '',
    input: (json['input'] as Map<String, dynamic>?),
    error: json['error'] as String?,
    output: json['output'] as String?,
    title: json['title'] as String?,
    metadata: json['metadata'] as Map<String, dynamic>?,
    completed: (json['completed'] as num?)?.toInt(),
  );

  static const pending = 'pending';
  static const running = 'running';
  static const statusCompleted = 'completed';
  static const statusError = 'error';

  final String status;
  final Map<String, dynamic>? input;
  final String? error;
  final String? output;
  final String? title;
  final Map<String, dynamic>? metadata;
  final int? completed;

  bool get isRunning => status == running;
  bool get isDone => status == statusCompleted;
  bool get isFailed => status == statusError;

  /// The task tool links its subagent session here — the click-through target.
  String? get subagentSessionID => metadata?['sessionID'] as String?;
}

class AssistantData {
  const AssistantData({
    required this.agent,
    required this.model,
    required this.created,
    this.completed,
    this.parts = const [],
    this.finish,
    this.tokens,
    this.cost,
    this.error,
  });
  factory AssistantData.fromJson(Map<String, dynamic> json) => AssistantData(
    agent: json['agent'] as String? ?? '',
    model: ModelRef.fromJson(
      (json['model'] as Map<String, dynamic>?) ?? const {},
    ),
    created: _intOrNull(json['time']?['created']) ?? 0,
    completed: _intOrNull(json['time']?['completed']),
    parts:
        ((json['content'] as List<dynamic>?)
            ?.whereType<Map<String, dynamic>>()
            .map(AssistantPart.fromJson)
            .toList()) ??
        const [],
    finish: json['finish'] as String?,
    tokens: json['tokens'] == null
        ? null
        : AssistantTokens.fromJson(json['tokens'] as Map<String, dynamic>),
    cost: (json['cost'] as num?)?.toDouble(),
    error: json['error'] == null
        ? null
        : AssistantError.fromJson(json['error'] as Map<String, dynamic>),
  );

  final String agent;
  final ModelRef model;
  final int created;
  final int? completed;
  final List<AssistantPart> parts;
  final String? finish;
  final AssistantTokens? tokens;
  final double? cost;
  final AssistantError? error;
}

class QueuedPrompt {
  const QueuedPrompt({
    required this.id,
    required this.text,
    required this.delivery,
    required this.timeCreated,
    this.files = const [],
  });

  factory QueuedPrompt.fromJson(Map<String, dynamic> json) => QueuedPrompt(
    id: json['id'] as String? ?? '',
    text: json['text'] as String? ?? '',
    delivery: json['delivery'] as String? ?? 'queue',
    timeCreated: (json['timeCreated'] as num?)?.toInt() ?? 0,
    files:
        ((json['files'] as List<dynamic>?)
            ?.whereType<Map<String, dynamic>>()
            .map(FileAttachment.fromJson)
            .toList()) ??
        const [],
  );

  final String id;
  final String text;
  final List<FileAttachment> files;

  /// "queue" waits for the current turn; "steer" joins it at the next step.
  final String delivery;
  final int timeCreated;
}

class Todo {
  const Todo({
    required this.content,
    required this.status,
    required this.priority,
    required this.position,
  });

  factory Todo.fromJson(Map<String, dynamic> json) => Todo(
    content: json['content'] as String? ?? '',
    status: json['status'] as String? ?? 'pending',
    priority: json['priority'] as String? ?? 'medium',
    position: (json['position'] as num?)?.toInt() ?? 0,
  );

  final String content;
  final String status;
  final String priority;
  final int position;

  bool get isCompleted => status == 'completed';
  bool get isInProgress => status == 'in_progress';
}

class SessionStats {
  const SessionStats({
    required this.cost,
    required this.tokensInput,
    required this.tokensOutput,
    required this.tokensReasoning,
    required this.tokensCacheRead,
    required this.tokensCacheWrite,
    required this.messages,
  });

  factory SessionStats.fromJson(Map<String, dynamic> json) => SessionStats(
    cost: (json['cost'] as num?)?.toDouble() ?? 0,
    tokensInput: (json['tokensInput'] as num?)?.toInt() ?? 0,
    tokensOutput: (json['tokensOutput'] as num?)?.toInt() ?? 0,
    tokensReasoning: (json['tokensReasoning'] as num?)?.toInt() ?? 0,
    tokensCacheRead: (json['tokensCacheRead'] as num?)?.toInt() ?? 0,
    tokensCacheWrite: (json['tokensCacheWrite'] as num?)?.toInt() ?? 0,
    messages: (json['messages'] as num?)?.toInt() ?? 0,
  );

  final double cost;
  final int tokensInput;
  final int tokensOutput;
  final int tokensReasoning;
  final int tokensCacheRead;
  final int tokensCacheWrite;
  final int messages;

  int get tokensTotal =>
      tokensInput +
      tokensOutput +
      tokensReasoning +
      tokensCacheRead +
      tokensCacheWrite;
}

class PermissionRequest {
  const PermissionRequest({
    required this.id,
    required this.sessionID,
    required this.action,
    required this.resources,
    this.agent,
    this.save = const [],
    this.metadata = const {},
    this.source,
  });

  factory PermissionRequest.fromJson(Map<String, dynamic> json) =>
      PermissionRequest(
        id: json['id'] as String? ?? '',
        sessionID: json['sessionID'] as String? ?? '',
        agent: json['agent'] as String?,
        action: json['action'] as String? ?? '',
        resources:
            (json['resources'] as List<dynamic>?)
                ?.whereType<String>()
                .toList() ??
            const [],
        save:
            (json['save'] as List<dynamic>?)?.whereType<String>().toList() ??
            const [],
        metadata: (json['metadata'] as Map<String, dynamic>?) ?? const {},
        source: json['source'] == null
            ? null
            : AskSource.fromJson(json['source'] as Map<String, dynamic>),
      );

  final String id;
  final String sessionID;
  final String? agent;
  final String action;
  final List<String> resources;
  final List<String> save;
  final Map<String, dynamic> metadata;
  final AskSource? source;

  /// Human-readable detail: the diff for edits, the command for bash.
  String get detail {
    if (metadata.isNotEmpty) {
      for (final key in const ['diff', 'command', 'url', 'commandName']) {
        final value = metadata[key];
        if (value is String && value.isNotEmpty) return value;
      }
    }
    return resources.join('\n');
  }
}

class AskSource {
  const AskSource({required this.type, this.messageID, this.callID});

  factory AskSource.fromJson(Map<String, dynamic> json) => AskSource(
    type: json['type'] as String? ?? '',
    messageID: json['messageID'] as String?,
    callID: json['callID'] as String?,
  );

  final String type;
  final String? messageID;
  final String? callID;
}

class QuestionOption {
  const QuestionOption({required this.label, this.description});

  factory QuestionOption.fromJson(Map<String, dynamic> json) => QuestionOption(
    label: json['label'] as String? ?? '',
    description: json['description'] as String?,
  );

  final String label;
  final String? description;
}

class QuestionPrompt {
  const QuestionPrompt({
    required this.question,
    required this.header,
    required this.options,
    this.multiple = false,
  });

  factory QuestionPrompt.fromJson(Map<String, dynamic> json) => QuestionPrompt(
    question: json['question'] as String? ?? '',
    header: json['header'] as String? ?? '',
    options:
        ((json['options'] as List<dynamic>?)
            ?.whereType<Map<String, dynamic>>()
            .map(QuestionOption.fromJson)
            .toList()) ??
        const [],
    multiple: json['multiple'] as bool? ?? false,
  );

  final String question;
  final String header;
  final List<QuestionOption> options;
  final bool multiple;
}

class QuestionRequest {
  const QuestionRequest({
    required this.id,
    required this.sessionID,
    required this.questions,
    this.source,
  });

  factory QuestionRequest.fromJson(Map<String, dynamic> json) =>
      QuestionRequest(
        id: json['id'] as String? ?? '',
        sessionID: json['sessionID'] as String? ?? '',
        questions:
            ((json['questions'] as List<dynamic>?)
                ?.whereType<Map<String, dynamic>>()
                .map(QuestionPrompt.fromJson)
                .toList()) ??
            const [],
        source: json['source'] == null
            ? null
            : AskSource.fromJson(json['source'] as Map<String, dynamic>),
      );

  final String id;
  final String sessionID;
  final List<QuestionPrompt> questions;
  final AskSource? source;
}

class ModelEntry {
  const ModelEntry({
    required this.providerID,
    required this.id,
    required this.name,
    this.contextLimit = 0,
    this.costInput = 0,
    this.variants = const [],
  });

  factory ModelEntry.fromJson(Map<String, dynamic> json) => ModelEntry(
    providerID: json['providerID'] as String? ?? '',
    id: json['id'] as String? ?? '',
    name: json['name'] as String? ?? '',
    contextLimit: (json['contextLimit'] as num?)?.toInt() ?? 0,
    costInput: (json['costInput'] as num?)?.toDouble() ?? 0,
    variants:
        (json['variants'] as List<dynamic>?)?.whereType<String>().toList() ??
        const [],
  );

  final String providerID;
  final String id;
  final String name;

  /// models.dev `limit.context`; 0 means unknown.
  final int contextLimit;

  /// models.dev `cost.input` (dollars per million input tokens).
  final double costInput;
  final List<String> variants;

  String get key => '$providerID/$id';
}

class Provider {
  const Provider({
    required this.id,
    required this.name,
    this.connected = false,
    this.available = false,
  });

  factory Provider.fromJson(Map<String, dynamic> json) => Provider(
    id: json['id'] as String? ?? '',
    name: json['name'] as String? ?? '',
    connected: json['connected'] as bool? ?? false,
    available: json['available'] as bool? ?? false,
  );

  final String id;
  final String name;
  final bool connected;
  final bool available;
}

class AuthMethod {
  const AuthMethod({
    required this.type,
    required this.label,
    this.env = const [],
    this.satisfied = false,
    this.prompts = const [],
  });

  factory AuthMethod.fromJson(Map<String, dynamic> json) => AuthMethod(
    type: json['type'] as String? ?? '',
    label: json['label'] as String? ?? '',
    env:
        (json['env'] as List<dynamic>?)?.whereType<String>().toList() ??
        const [],
    satisfied: json['satisfied'] as bool? ?? false,
    prompts:
        ((json['prompts'] as List<dynamic>?)
            ?.whereType<Map<String, dynamic>>()
            .map(AuthPrompt.fromJson)
            .toList()) ??
        const [],
  );

  final String type;
  final String label;
  final List<String> env;
  final bool satisfied;
  final List<AuthPrompt> prompts;
}

class AuthPrompt {
  const AuthPrompt({
    required this.key,
    required this.label,
    this.options = const [],
  });

  factory AuthPrompt.fromJson(Map<String, dynamic> json) => AuthPrompt(
    key: json['key'] as String? ?? '',
    label: json['label'] as String? ?? '',
    options:
        (json['options'] as List<dynamic>?)?.whereType<String>().toList() ??
        const [],
  );

  final String key;
  final String label;
  final List<String> options;
}

class OAuthAttempt {
  const OAuthAttempt({
    required this.id,
    required this.url,
    required this.code,
    required this.status,
    this.error,
  });

  factory OAuthAttempt.fromJson(Map<String, dynamic> json) => OAuthAttempt(
    id: json['id'] as String? ?? '',
    url: json['url'] as String? ?? '',
    code: json['code'] as String? ?? '',
    status: json['status'] as String? ?? '',
    error: json['error'] as String?,
  );

  final String id;
  final String url;
  final String code;
  final String status;
  final String? error;

  bool get done =>
      status == 'done' || status == 'complete' || status == 'succeeded';
}

class Agent {
  const Agent({
    required this.id,
    required this.mode,
    this.description,
    this.hidden = false,
  });

  factory Agent.fromJson(Map<String, dynamic> json) => Agent(
    id: json['id'] as String? ?? '',
    mode: json['mode'] as String? ?? '',
    description: json['description'] as String?,
    hidden: json['hidden'] as bool? ?? false,
  );

  final String id;
  final String mode;
  final String? description;
  final bool hidden;
}

class Command {
  const Command({
    required this.name,
    required this.template,
    this.description,
    this.agent,
    this.model,
    this.subtask = false,
    this.source,
    this.hints = const [],
  });

  factory Command.fromJson(Map<String, dynamic> json) => Command(
    name: json['name'] as String? ?? '',
    description: json['description'] as String?,
    agent: json['agent'] as String?,
    model: json['model'] as String?,
    subtask: json['subtask'] as bool? ?? false,
    template: json['template'] as String? ?? '',
    source: json['source'] as String?,
    hints:
        (json['hints'] as List<dynamic>?)?.whereType<String>().toList() ??
        const [],
  );

  final String name;
  final String? description;
  final String? agent;
  final String? model;
  final bool subtask;
  final String template;
  final String? source;
  final List<String> hints;
}

class Skill {
  const Skill({
    required this.name,
    required this.location,
    this.description,
    this.slash = false,
  });

  factory Skill.fromJson(Map<String, dynamic> json) => Skill(
    name: json['name'] as String? ?? '',
    description: json['description'] as String?,
    slash: json['slash'] as bool? ?? false,
    location: json['location'] as String? ?? '',
  );

  final String name;
  final String? description;
  final bool slash;
  final String location;
}

class VcsInfo {
  const VcsInfo({this.branch, this.defaultBranch});

  factory VcsInfo.fromJson(Map<String, dynamic> json) => VcsInfo(
    branch: json['branch'] as String?,
    defaultBranch: json['defaultBranch'] as String?,
  );

  final String? branch;
  final String? defaultBranch;

  bool get inRepo => branch != null && branch!.isNotEmpty;
}

class FileDiff {
  const FileDiff({
    required this.file,
    required this.patch,
    required this.additions,
    required this.deletions,
    required this.status,
  });

  factory FileDiff.fromJson(Map<String, dynamic> json) => FileDiff(
    file: json['file'] as String? ?? '',
    patch: json['patch'] as String? ?? '',
    additions: (json['additions'] as num?)?.toInt() ?? 0,
    deletions: (json['deletions'] as num?)?.toInt() ?? 0,
    status: json['status'] as String? ?? '',
  );

  final String file;
  final String patch;
  final int additions;
  final int deletions;
  final String status;
}

class McpServer {
  const McpServer({required this.name, required this.status, this.error});

  factory McpServer.fromJson(Map<String, dynamic> json) => McpServer(
    name: json['name'] as String? ?? '',
    status: json['status'] as String? ?? '',
    error: json['error'] as String?,
  );

  final String name;
  final String status;
  final String? error;

  bool get connected => status == 'connected';
}

class LspServer {
  const LspServer({
    required this.id,
    required this.name,
    required this.root,
    required this.status,
  });

  factory LspServer.fromJson(Map<String, dynamic> json) => LspServer(
    id: json['id'] as String? ?? '',
    name: json['name'] as String? ?? '',
    root: json['root'] as String? ?? '',
    status: json['status'] as String? ?? '',
  );

  final String id;
  final String name;
  final String root;
  final String status;
}

class LspState {
  const LspState({
    required this.enabled,
    required this.servers,
    required this.available,
  });

  factory LspState.fromJson(Map<String, dynamic> json) => LspState(
    enabled: json['enabled'] as bool? ?? false,
    servers:
        ((json['servers'] as List<dynamic>?)
            ?.whereType<Map<String, dynamic>>()
            .map(LspServer.fromJson)
            .toList()) ??
        const [],
    available:
        (json['available'] as List<dynamic>?)?.whereType<String>().toList() ??
        const [],
  );

  final bool enabled;
  final List<LspServer> servers;
  final List<String> available;
}

class Memory {
  const Memory({
    required this.id,
    required this.scope,
    required this.content,
    required this.origin,
    this.category,
    this.pinned = false,
    this.disabled = false,
  });

  factory Memory.fromJson(Map<String, dynamic> json) => Memory(
    id: json['id'] as String? ?? '',
    scope: json['scope'] as String? ?? '',
    content: json['content'] as String? ?? '',
    category: json['category'] as String?,
    origin: json['origin'] as String? ?? '',
    pinned: json['pinned'] as bool? ?? false,
    disabled: json['disabled'] as bool? ?? false,
  );

  final String id;
  final String scope;
  final String content;
  final String? category;
  final String origin;
  final bool pinned;
  final bool disabled;
}

/// A committed event from the /api/event SSE stream.
class ApiEvent {
  const ApiEvent({
    required this.id,
    required this.type,
    this.data = const {},
    this.seq,
    this.sessionID,
  });

  factory ApiEvent.fromJson(Map<String, dynamic> json) => ApiEvent(
    id: json['id'] as String? ?? '',
    type: json['type'] as String? ?? '',
    data: (json['data'] as Map<String, dynamic>?) ?? const {},
    seq: (json['seq'] as num?)?.toInt(),
    sessionID: json['sessionID'] as String?,
  );

  final String id;
  final String type;
  final Map<String, dynamic> data;
  final int? seq;
  final String? sessionID;
}

int? _intOrNull(Object? value) => value is num ? value.toInt() : null;

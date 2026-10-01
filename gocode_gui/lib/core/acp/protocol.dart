/// ACP wire types — the subset of Agent Client Protocol v1
/// (agentclientprotocol.com) that gocode's agent serves and this client
/// uses. Field names follow the protocol's camelCase shapes; decoding is
/// tolerant by design: an unknown field is ignored, a missing one falls
/// back to its documented default.
library;

/// One selectable mode (agent) from the initialize / setup response.
class AcpMode {
  const AcpMode({required this.id, required this.name, this.description});

  factory AcpMode.fromJson(Map<String, dynamic> json) => AcpMode(
    id: json['id'] as String? ?? '',
    name: json['name'] as String? ?? json['id'] as String? ?? '',
    description: json['description'] as String?,
  );

  final String id;
  final String name;
  final String? description;
}

/// One option of a [AcpConfigOption] (or a nested group's list).
class AcpOptionValue {
  const AcpOptionValue({
    required this.value,
    required this.name,
    this.description,
  });

  factory AcpOptionValue.fromJson(Map<String, dynamic> json) =>
      AcpOptionValue(
        value: json['value'] as String? ?? '',
        name: json['name'] as String? ?? json['value'] as String? ?? '',
        description: json['description'] as String?,
      );

  final String value;
  final String name;
  final String? description;
}

/// A group inside a select option's value list (v1 "group" key).
class AcpOptionGroup {
  const AcpOptionGroup({required this.id, required this.name});

  factory AcpOptionGroup.fromJson(Map<String, dynamic> json) =>
      AcpOptionGroup(
        id: json['group'] as String? ?? '',
        name: json['name'] as String? ?? json['group'] as String? ?? '',
      );

  final String id;
  final String name;
}

/// One config option from the session setup state (mode, model, reasoning
/// level, auto-approve). The value list is flat: groups carry their
/// provider's name so a picker can re-group them.
class AcpConfigOption {
  AcpConfigOption({
    required this.id,
    required this.name,
    required this.type,
    this.category,
    this.description,
    this.currentValue,
    List<AcpOptionValue>? values,
    List<AcpOptionGroup>? groups,
  }) : values = values ?? [],
       groups = groups ?? [];

  factory AcpConfigOption.fromJson(Map<String, dynamic> json) {
    final rawOptions = (json['options'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>()
        .toList();

    // Options whose type is "group" carry a nested list (v1 shape).
    final values = <AcpOptionValue>[];
    final groups = <AcpOptionGroup>[];
    for (final option in rawOptions) {
      if (option['options'] is List) {
        final group = AcpOptionGroup.fromJson(option);
        groups.add(group);
        for (final nested
            in (option['options'] as List<dynamic>)
                .whereType<Map<String, dynamic>>()) {
          final value = AcpOptionValue.fromJson(nested);
          values.add(
            AcpOptionValue(
              value: value.value,
              name: value.name,
              description: group.name,
            ),
          );
        }
      } else {
        values.add(AcpOptionValue.fromJson(option));
      }
    }

    return AcpConfigOption(
      id: json['id'] as String? ?? '',
      name: json['name'] as String? ?? json['id'] as String? ?? '',
      type: json['type'] as String? ?? 'select',
      category: json['category'] as String?,
      description: json['description'] as String?,
      currentValue: json['currentValue'],
      values: values,
      groups: groups,
    );
  }

  final String id;
  final String name;
  final String type;
  final String? category;
  final String? description;

  /// The id (select) or bool (boolean) currently in effect.
  final Object? currentValue;

  final List<AcpOptionValue> values;
  final List<AcpOptionGroup> groups;

  bool get isBoolean => type == 'boolean';
}

/// One file location a tool call reports (v1 Location).
class AcpToolLocation {
  const AcpToolLocation({required this.path, this.line});

  factory AcpToolLocation.fromJson(Map<String, dynamic> json) =>
      AcpToolLocation(
        path: json['path'] as String? ?? '',
        line: (json['line'] as num?)?.toInt(),
      );

  final String path;
  final int? line;
}

/// The stable shape of one session/update, already stripped of the envelope
/// ({sessionId, update}).
class AcpSessionUpdate {
  AcpSessionUpdate(this.raw);

  final Map<String, dynamic> raw;

  String get kind => raw['sessionUpdate'] as String? ?? '';
  String get sessionID => raw['sessionId'] as String? ?? '';
  String get messageID => raw['messageId'] as String? ?? '';
  String get toolCallID => raw['toolCallId'] as String? ?? '';

  /// Content blocks (chunks carry one; whole messages carry a list).
  List<Map<String, dynamic>> get contentBlocks {
    final content = raw['content'];
    if (content is Map<String, dynamic>) return [content];
    if (content is List) {
      return content.whereType<Map<String, dynamic>>().toList();
    }
    return const [];
  }

  /// v1 chunks wrap their text in {"type": "text", "text": …}.
  String get text {
    final block = contentBlocks.firstOrNull;
    if (block == null) return '';
    return block['text'] as String? ?? '';
  }
}

/// One selectable model from an ACP config option; mirrors the fields a
/// picker needs from [ModelEntry] without depending on the API models.
class ModelRow {
  const ModelRow({
    required this.value,
    required this.name,
    this.providerName,
  });

  /// The full `provider/model` option value.
  final String value;
  final String name;

  /// The group (provider display name) the value came from.
  final String? providerName;

  String get key => value;
}

/// The model option's values as picker rows, decoded from the untyped
/// [SessionState.acpConfigOptions] payload.
List<ModelRow> acpModelRows(List<Object> options) {
  for (final entry in options) {
    if (entry is! AcpConfigOption || entry.id != 'model') continue;
    return [
      for (final value in entry.values)
        ModelRow(
          value: value.value,
          name: value.name,
          providerName: value.description,
        ),
    ];
  }
  return const [];
}

/// The thinking levels of a session's thought_level option, decoded from
/// the untyped [SessionState.acpConfigOptions] payload.
List<String> acpThoughtLevels(List<Object> options) {
  for (final entry in options) {
    if (entry is! AcpConfigOption || entry.id != 'thought_level') continue;
    return [
      for (final value in entry.values)
        if (value.value != 'default') value.value,
    ];
  }
  return const [];
}

/// One session entry from session/list.
class AcpSessionEntry {
  const AcpSessionEntry({
    required this.id,
    required this.cwd,
    this.title,
    required this.updatedAt,
  });

  factory AcpSessionEntry.fromJson(Map<String, dynamic> json) =>
      AcpSessionEntry(
        id: json['sessionId'] as String? ?? '',
        cwd: json['cwd'] as String? ?? '',
        title: json['title'] as String?,
        updatedAt: DateTime.tryParse(json['updatedAt'] as String? ?? '') ??
            DateTime.fromMillisecondsSinceEpoch(0),
      );

  final String id;
  final String cwd;
  final String? title;
  final DateTime updatedAt;
}

/// A permission option the agent offered.
class AcpPermissionOption {
  const AcpPermissionOption({
    required this.id,
    required this.name,
    required this.kind,
  });

  factory AcpPermissionOption.fromJson(Map<String, dynamic> json) =>
      AcpPermissionOption(
        id: json['optionId'] as String? ?? '',
        name: json['name'] as String? ?? '',
        kind: json['kind'] as String? ?? '',
      );

  final String id;
  final String name;

  /// allow_once | allow_always | reject_once
  final String kind;
}

/// A session/request_permission call from the agent, with the v1 toolCall
/// patch carrying the diff / subject.
class AcpPermissionRequest {
  AcpPermissionRequest({
    required this.sessionID,
    required this.options,
    required this.toolCall,
  });

  final String sessionID;
  final List<AcpPermissionOption> options;
  final Map<String, dynamic> toolCall;

  /// The ask's one-line question (the toolCall patch's title).
  String get title => toolCall['title'] as String? ?? 'Permission';

  /// The diff block, when the ask carries one.
  String? get diffText {
    for (final block in (toolCall['content'] as List<dynamic>? ?? const [])) {
      if (block is Map<String, dynamic> &&
          block['type'] == 'diff' &&
          block['newText'] is String) {
        final old = (block['oldText'] as String?) ?? '';
        return _renderDiff(old, block['newText'] as String? ?? '');
      }
    }
    return null;
  }

  static String _renderDiff(String oldText, String newText) {
    // A minimal whole-file before/after renderer: shared prefix/suffix
    // shrink to the changed middle. The dedicated diff view re-renders.
    final oldLines = oldText.split('\n');
    final newLines = newText.split('\n');
    var start = 0;
    while (start < oldLines.length &&
        start < newLines.length &&
        oldLines[start] == newLines[start]) {
      start++;
    }
    var endOld = oldLines.length;
    var endNew = newLines.length;
    while (endOld > start &&
        endNew > start &&
        oldLines[endOld - 1] == newLines[endNew - 1]) {
      endOld--;
      endNew--;
    }
    final out = StringBuffer();
    for (final line in oldLines.sublist(start, endOld)) {
      out.write('- $line\n');
    }
    for (final line in newLines.sublist(start, endNew)) {
      out.write('+ $line\n');
    }
    return out.toString();
  }
}

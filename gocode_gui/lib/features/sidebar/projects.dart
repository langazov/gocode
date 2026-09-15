import 'dart:async';
import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// A named work folder the sidebar groups sessions under.
///
/// Purely a client-side concept: gocode itself has no project CRUD API, only
/// a directory-derived `Session.projectID` it assigns automatically. A
/// [Project] just remembers a name for a folder the user picked; sessions
/// "belong" to it by matching [directory] exactly (see [groupByProject]).
class Project {
  const Project({
    required this.id,
    required this.name,
    required this.directory,
    required this.timeCreated,
  });

  final String id;
  final String name;
  final String directory;
  final int timeCreated;

  Project copyWith({String? name, String? directory}) => Project(
    id: id,
    name: name ?? this.name,
    directory: directory ?? this.directory,
    timeCreated: timeCreated,
  );

  Map<String, dynamic> toJson() => {
    'id': id,
    'name': name,
    'directory': directory,
    'timeCreated': timeCreated,
  };

  factory Project.fromJson(Map<String, dynamic> json) => Project(
    id: json['id'] as String? ?? '',
    name: json['name'] as String? ?? '',
    directory: json['directory'] as String? ?? '',
    timeCreated: (json['timeCreated'] as num?)?.toInt() ?? 0,
  );
}

/// The last path segment, for defaulting an unnamed project's title.
String nameFromDirectory(String directory) {
  final trimmed = directory.replaceAll(RegExp(r'[/\\]+$'), '');
  final segment = trimmed.split(RegExp(r'[/\\]')).lastWhere(
    (s) => s.isNotEmpty,
    orElse: () => directory,
  );
  return segment;
}

const _prefsKey = 'projects.v1';

/// The user's projects, persisted locally and loaded once at startup.
class ProjectsNotifier extends Notifier<List<Project>> {
  @override
  List<Project> build() {
    unawaited(_load());
    return const [];
  }

  Future<void> _load() async {
    final prefs = await SharedPreferences.getInstance();
    final raw = prefs.getStringList(_prefsKey) ?? const [];
    final loaded = [
      for (final entry in raw)
        Project.fromJson(jsonDecode(entry) as Map<String, dynamic>),
    ]..sort((a, b) => b.timeCreated.compareTo(a.timeCreated));
    state = loaded;
  }

  Future<void> _persist() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setStringList(_prefsKey, [
      for (final p in state) jsonEncode(p.toJson()),
    ]);
  }

  /// Creates a project rooted at [directory]. Returns it so the caller (the
  /// new-project dialog) can select it immediately.
  Future<Project> create({required String name, required String directory}) async {
    final project = Project(
      id: 'proj_${DateTime.now().microsecondsSinceEpoch}',
      name: name.trim().isEmpty ? nameFromDirectory(directory) : name.trim(),
      directory: directory,
      timeCreated: DateTime.now().millisecondsSinceEpoch,
    );
    state = [project, ...state];
    await _persist();
    return project;
  }

  Future<void> rename(String id, String name) async {
    if (name.trim().isEmpty) return;
    state = [
      for (final p in state)
        if (p.id == id) p.copyWith(name: name.trim()) else p,
    ];
    await _persist();
  }

  Future<void> delete(String id) async {
    state = state.where((p) => p.id != id).toList();
    await _persist();
  }
}

final projectsProvider = NotifierProvider<ProjectsNotifier, List<Project>>(
  ProjectsNotifier.new,
);

/// The project with [id], or null if unset or no longer known (e.g. removed
/// from another window).
Project? findProject(List<Project> projects, String? id) {
  if (id == null) return null;
  for (final p in projects) {
    if (p.id == id) return p;
  }
  return null;
}

/// The project new sessions attach to, by prefilling its directory — set by
/// selecting a project in the sidebar. Sticky: it stays selected across
/// multiple new chats until the user picks a different project or clears it
/// explicitly, so a run of chats can land in the same place.
class SelectedProjectNotifier extends Notifier<String?> {
  @override
  String? build() => null;

  void select(String id) => state = id;
  void clear() => state = null;
}

final selectedProjectProvider =
    NotifierProvider<SelectedProjectNotifier, String?>(
      SelectedProjectNotifier.new,
    );

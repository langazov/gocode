import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/models.dart';
import '../../core/connection/controller.dart';
import '../session/timeline.dart' show acpConnectionProvider;

/// Sessions list, re-fetched when the connection, the ACP attachment's own
/// change signal, or an invalidation changes.
final sessionsProvider = FutureProvider.autoDispose<List<Session>>((ref) async {
  final acp = ref.watch(acpConnectionProvider);
  if (acp != null) {
    final changed = StreamController<void>.broadcast();
    final sub = acp.sessionsChanged.listen(changed.add);
    ref.onDispose(() {
      unawaited(sub.cancel());
      unawaited(changed.close());
    });
    return acp.refreshSessions();
  }
  final client = ref.watch(apiClientProvider);
  if (client == null) return const <Session>[];
  return client.sessions();
});

/// Available models for pickers. In ACP mode there is no standalone model
/// list: the model config option of the last setup is the catalog.
final modelsProvider = FutureProvider.autoDispose<List<ModelEntry>>((
  ref,
) async {
  final acp = ref.watch(acpConnectionProvider);
  if (acp != null) {
    for (final option in acp.configOptions) {
      if (option.id == 'model') {
        return [
          for (final value in option.values)
            ModelEntry(
              providerID: value.value.split('/').first,
              id: value.value.split('/').skip(1).join('/'),
              name: value.name,
              // description carries the group (provider display name).
            ),
        ];
      }
    }
    return const <ModelEntry>[];
  }
  final client = ref.watch(apiClientProvider);
  if (client == null) return const <ModelEntry>[];
  return client.models();
});

/// Available agents for pickers. In ACP mode the modes of the last setup
/// are the agents.
final agentsProvider = FutureProvider.autoDispose<List<Agent>>((ref) async {
  final acp = ref.watch(acpConnectionProvider);
  if (acp != null) {
    return [
      for (final mode in acp.modes)
        Agent(id: mode.id, mode: 'primary', description: mode.description),
    ];
  }
  final client = ref.watch(apiClientProvider);
  if (client == null) return const <Agent>[];
  return client.agents();
});

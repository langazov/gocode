import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/models.dart';
import '../../core/connection/controller.dart';

/// Sessions list, re-fetched when the connection or an invalidation changes.
final sessionsProvider = FutureProvider.autoDispose<List<Session>>((ref) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) return const <Session>[];
  return client.sessions();
});

/// Available models for pickers.
final modelsProvider = FutureProvider.autoDispose<List<ModelEntry>>((
  ref,
) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) return const <ModelEntry>[];
  return client.models();
});

/// Available agents for pickers.
final agentsProvider = FutureProvider.autoDispose<List<Agent>>((ref) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) return const <Agent>[];
  return client.agents();
});

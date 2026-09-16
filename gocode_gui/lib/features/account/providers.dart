import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/account.dart';
import '../../core/api/client.dart';
import '../../core/connection/controller.dart';

/// The event the server publishes when this machine's sign-in changes —
/// through it, from the TUI, or with `gocode login`.
const accountUpdatedEvent = 'account.updated';

/// Fires whenever who is signed in may have changed: the server announced
/// it, or the event stream reconnected and may have dropped the
/// announcement.
final accountChangesProvider = Provider<Stream<void>>((ref) {
  // The client appears once connected, which is when the controller's event
  // stream exists; watching it re-subscribes on every new connection.
  if (ref.watch(apiClientProvider) == null) return const Stream.empty();
  final controller = ref.read(connectionControllerProvider);
  if (controller == null) return const Stream.empty();
  final changes = StreamController<void>.broadcast();
  final subs = [
    controller.events
        .where((e) => e.type == accountUpdatedEvent)
        .listen((_) => changes.add(null)),
    controller.reconnectSignal.listen(changes.add),
  ];
  ref.onDispose(() {
    for (final sub in subs) {
      unawaited(sub.cancel());
    }
    unawaited(changes.close());
  });
  return changes.stream;
});

/// Refetches the calling provider whenever [accountChangesProvider] fires.
void _refetchOnAccountChange(Ref ref) {
  final sub = ref
      .watch(accountChangesProvider)
      .listen((_) => ref.invalidateSelf());
  ref.onDispose(() => unawaited(sub.cancel()));
}

/// Who the server is signed in to gocoder.org as; null while disconnected.
/// Kept alive: the sidebar's account button always shows it.
final accountProvider = FutureProvider<AccountInfo?>((ref) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) return null;
  _refetchOnAccountChange(ref);
  return client.account();
});

/// Usage over the last `days` days.
final usageProvider = FutureProvider.autoDispose.family<UsageSummary, int>((
  ref,
  days,
) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) throw StateError('not connected to a gocode server');
  _refetchOnAccountChange(ref);
  return client.accountUsage(days: days);
});

/// The account's invite link and referral count.
final inviteProvider = FutureProvider.autoDispose<InviteInfo>((ref) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) throw StateError('not connected to a gocode server');
  _refetchOnAccountChange(ref);
  return client.accountInvite();
});

/// The server's own message for an account failure ("invalid email or
/// password"), rather than the exception's type name.
String accountErrorText(Object error) =>
    error is ApiException ? error.message : '$error';

GocodeClient _client(WidgetRef ref) {
  final client = ref.read(apiClientProvider);
  if (client == null) throw StateError('not connected to a gocode server');
  return client;
}

/// Everything that depends on who is signed in.
void _refresh(WidgetRef ref) {
  ref
    ..invalidate(accountProvider)
    ..invalidate(usageProvider)
    ..invalidate(inviteProvider);
}

Future<void> signInToAccount(
  WidgetRef ref,
  String email,
  String password,
) async {
  await _client(ref).signIn(email, password);
  _refresh(ref);
}

Future<void> signOutOfAccount(WidgetRef ref) async {
  await _client(ref).signOut();
  _refresh(ref);
}

Future<void> renameAccount(WidgetRef ref, String displayName) async {
  await _client(ref).renameAccount(displayName);
  ref.invalidate(accountProvider);
}

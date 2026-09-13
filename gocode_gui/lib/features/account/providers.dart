import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/account.dart';
import '../../core/api/client.dart';
import '../../core/connection/controller.dart';

/// Who the server is signed in to gocoder.org as; null while disconnected.
/// Kept alive: the sidebar's account button always shows it.
final accountProvider = FutureProvider<AccountInfo?>((ref) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) return null;
  return client.account();
});

/// Usage over the last `days` days.
final usageProvider = FutureProvider.autoDispose.family<UsageSummary, int>((
  ref,
  days,
) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) throw StateError('not connected to a gocode server');
  return client.accountUsage(days: days);
});

/// The account's invite link and referral count.
final inviteProvider = FutureProvider.autoDispose<InviteInfo>((ref) async {
  final client = ref.watch(apiClientProvider);
  if (client == null) throw StateError('not connected to a gocode server');
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

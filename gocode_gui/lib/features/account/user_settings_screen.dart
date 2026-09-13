import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/api/account.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/page_frame.dart';
import 'account_widgets.dart';
import 'providers.dart';
import 'sign_in_panel.dart';

/// Settings for the gocoder.org account (the app's own options live under
/// Settings): display name, password and deletion on the website, and
/// signing this machine out.
class UserSettingsScreen extends ConsumerWidget {
  const UserSettingsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final account = ref.watch(accountProvider);
    return PageFrame(
      caption: 'Account',
      title: 'User settings',
      lead:
          'Your gocoder.org account. For this app’s own options, see '
          'Settings.',
      children: [
        account.when(
          loading: () => const Center(
            child: Padding(
              padding: EdgeInsets.all(32),
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
          ),
          error: (e, _) => ErrorPanel(
            message: accountErrorText(e),
            onRetry: () => ref.invalidate(accountProvider),
          ),
          data: (info) => info == null || !info.signedIn || info.expired
              ? SignInPanel(
                  message: 'Manage your account from this machine.',
                  registerUrl: info?.page('/register'),
                )
              : _Settings(info: info),
        ),
      ],
    );
  }
}

class _Settings extends ConsumerStatefulWidget {
  const _Settings({required this.info});

  final AccountInfo info;

  @override
  ConsumerState<_Settings> createState() => _SettingsState();
}

class _SettingsState extends ConsumerState<_Settings> {
  late final _name = TextEditingController(text: widget.info.displayName);
  bool _saving = false;
  bool _saved = false;
  String? _error;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    final name = _name.text.trim();
    if (name.isEmpty || _saving) return;
    setState(() {
      _saving = true;
      _saved = false;
      _error = null;
    });
    try {
      await renameAccount(ref, name);
      if (mounted) setState(() => _saved = true);
    } catch (e) {
      if (mounted) setState(() => _error = accountErrorText(e));
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _signOut() async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Sign out of gocoder.org?'),
        content: const Text(
          "This machine's API key is revoked, and settings sync stops until "
          'you sign in again.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Sign out'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;
    try {
      await signOutOfAccount(ref);
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(accountErrorText(e))));
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final info = widget.info;
    final danger = OutlinedButton.styleFrom(
      foregroundColor: GC.downText,
      iconColor: GC.downText,
      backgroundColor: Colors.transparent,
      side: BorderSide(color: GC.down.withValues(alpha: 0.45)),
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        AccountSection(
          title: 'Display name',
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Expanded(
                    child: TextField(
                      controller: _name,
                      onChanged: (_) => setState(() => _saved = false),
                      onSubmitted: (_) => _save(),
                      decoration: const InputDecoration(
                        hintText: 'How gocoder.org greets you',
                      ),
                    ),
                  ),
                  const SizedBox(width: 8),
                  FilledButton(
                    onPressed: _saving ? null : _save,
                    child: const Text('Save'),
                  ),
                ],
              ),
              if (_saved) ...[
                const SizedBox(height: 8),
                Text(
                  'Saved.',
                  style: theme.textTheme.bodySmall?.copyWith(color: GC.ok),
                ),
              ],
              if (_error != null) ...[
                const SizedBox(height: 10),
                ErrorPanel(message: _error!),
              ],
            ],
          ),
        ),
        const SizedBox(height: 16),
        AccountSection(
          title: 'Password',
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                'Passwords change on gocoder.org, in a signed-in browser. '
                "This machine's API key isn't allowed to change them.",
                style: theme.textTheme.bodyMedium,
              ),
              const SizedBox(height: 12),
              OutlinedButton.icon(
                onPressed: () => openExternal(info.page('/settings')),
                icon: const Icon(Icons.open_in_new_rounded, size: 18),
                label: const Text('Change password on gocoder.org'),
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        AccountSection(
          title: 'This machine',
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                'Signed in as ${info.email}'
                '${info.keyPrefix.isEmpty ? '' : ' with key ${info.keyPrefix}…'}.',
                style: theme.textTheme.bodyMedium,
              ),
              const SizedBox(height: 12),
              OutlinedButton.icon(
                style: danger,
                onPressed: _signOut,
                icon: const Icon(Icons.logout_rounded, size: 18),
                label: const Text('Sign out'),
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        AccountSection(
          title: 'Delete account',
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                'Deleting your account removes your API keys, usage history '
                'and synced settings. It happens on gocoder.org.',
                style: theme.textTheme.bodyMedium,
              ),
              const SizedBox(height: 12),
              OutlinedButton.icon(
                style: danger,
                onPressed: () => openExternal(info.page('/settings')),
                icon: const Icon(Icons.open_in_new_rounded, size: 18),
                label: const Text('Delete on gocoder.org'),
              ),
            ],
          ),
        ),
      ],
    );
  }
}

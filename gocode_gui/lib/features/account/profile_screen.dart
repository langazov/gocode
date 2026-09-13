import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/account.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/page_frame.dart';
import 'account_widgets.dart';
import 'avatar.dart';
import 'providers.dart';
import 'sign_in_panel.dart';

/// The gocoder.org account this machine is signed in to.
class ProfileScreen extends ConsumerWidget {
  const ProfileScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final account = ref.watch(accountProvider);
    return PageFrame(
      caption: 'Account',
      title: 'Profile',
      lead: 'Your gocoder.org account, as this machine is signed in to it.',
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
          data: (info) => info == null || !info.signedIn
              ? SignInPanel(
                  message: 'See your profile, usage and invite link.',
                  registerUrl: info?.page('/register'),
                )
              : _Profile(info: info),
        ),
      ],
    );
  }
}

class _Profile extends StatelessWidget {
  const _Profile({required this.info});

  final AccountInfo info;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final facts = [
      if (info.memberSince != null)
        'Member since ${monthYear(info.memberSince!)}',
      if (info.keyPrefix.isNotEmpty) 'Key ${info.keyPrefix}… on this machine',
      Uri.tryParse(info.site)?.host ?? info.site,
    ];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (info.expired) ...[
          const NoticePanel(
            icon: Icons.warning_amber_rounded,
            color: GC.warn,
            text:
                "This machine's sign-in was revoked or has expired. Sign in "
                'again to keep using your account here.',
          ),
          const SizedBox(height: 16),
          SignInPanel(
            message: 'Sign in again on this machine.',
            registerUrl: info.page('/register'),
          ),
          const SizedBox(height: 16),
        ],
        if (info.offline) ...[
          const NoticePanel(
            text:
                "gocoder.org can't be reached, so these are the details saved "
                'on this machine.',
          ),
          const SizedBox(height: 16),
        ],
        GlassSurface(
          blur: false,
          radius: GC.rPanel,
          padding: const EdgeInsets.all(24),
          child: Row(
            children: [
              AccountAvatar(initials: info.initials, size: 64),
              const SizedBox(width: 20),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(info.name, style: theme.textTheme.headlineSmall),
                    const SizedBox(height: 4),
                    Text(info.email, style: theme.textTheme.bodyMedium),
                    const SizedBox(height: 12),
                    Wrap(
                      spacing: 8,
                      runSpacing: 8,
                      children: [for (final fact in facts) _Fact(fact)],
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            OutlinedButton.icon(
              onPressed: () => context.go('/account/settings'),
              icon: const Icon(Icons.edit_outlined, size: 18),
              label: const Text('Edit profile'),
            ),
            OutlinedButton.icon(
              onPressed: () => openExternal(info.page('/dashboard')),
              icon: const Icon(Icons.open_in_new_rounded, size: 18),
              label: const Text('Open gocoder.org'),
            ),
          ],
        ),
      ],
    );
  }
}

class _Fact extends StatelessWidget {
  const _Fact(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: GC.borderStrong),
      ),
      child: Text(text, style: Theme.of(context).textTheme.labelMedium),
    );
  }
}

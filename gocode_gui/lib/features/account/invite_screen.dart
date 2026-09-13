import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/api/account.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/page_frame.dart';
import 'account_widgets.dart';
import 'providers.dart';
import 'sign_in_panel.dart';

/// The account's invite link, and how many friends joined through it.
class InviteScreen extends ConsumerWidget {
  const InviteScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final account = ref.watch(accountProvider);
    final signedIn = account.value?.signedIn ?? false;
    final expired = account.value?.expired ?? false;
    return PageFrame(
      caption: 'Account',
      title: 'Invite a friend',
      lead:
          'Share your link. Everyone who signs up with it counts as your '
          'invite.',
      children: [
        if (account.isLoading && account.value == null)
          const Center(
            child: Padding(
              padding: EdgeInsets.all(32),
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
          )
        else if (!signedIn || expired)
          SignInPanel(
            message: 'Get your invite link.',
            registerUrl: account.value?.page('/register'),
          )
        else
          ref
              .watch(inviteProvider)
              .when(
                loading: () => const Center(
                  child: Padding(
                    padding: EdgeInsets.all(32),
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                ),
                error: (e, _) => ErrorPanel(
                  message: accountErrorText(e),
                  onRetry: () => ref.invalidate(inviteProvider),
                ),
                data: (invite) => _Invite(invite: invite),
              ),
      ],
    );
  }
}

class _Invite extends StatelessWidget {
  const _Invite({required this.invite});

  final InviteInfo invite;

  Future<void> _copy(BuildContext context, String text, String done) async {
    await Clipboard.setData(ClipboardData(text: text));
    if (context.mounted) {
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(done)));
    }
  }

  @override
  Widget build(BuildContext context) {
    final message =
        "I've been using gocode, an AI coding assistant. Sign up with my "
        'invite: ${invite.url}';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        GlassSurface(
          blur: false,
          radius: GC.rPanel,
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const Caption('Your invite link'),
              const SizedBox(height: 10),
              CodeBlock(invite.url),
              const SizedBox(height: 16),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                children: [
                  FilledButton.icon(
                    onPressed: () =>
                        _copy(context, invite.url, 'Invite link copied'),
                    icon: const Icon(Icons.link_rounded, size: 18),
                    label: const Text('Copy link'),
                  ),
                  OutlinedButton.icon(
                    onPressed: () =>
                        _copy(context, message, 'Invite message copied'),
                    icon: const Icon(
                      Icons.chat_bubble_outline_rounded,
                      size: 18,
                    ),
                    label: const Text('Copy invite message'),
                  ),
                ],
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        Wrap(
          spacing: 12,
          runSpacing: 12,
          children: [
            StatTile(
              label: 'Friends joined',
              value: groupedNumber(invite.invited),
            ),
            StatTile(label: 'Your code', value: invite.code, mono: true),
          ],
        ),
      ],
    );
  }
}

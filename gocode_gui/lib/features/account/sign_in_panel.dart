import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../shared/widgets/glass.dart';
import 'account_widgets.dart';
import 'providers.dart';

/// Signs the server in to gocoder.org, shown wherever an account page needs
/// an account. On success the account providers refresh and the page swaps
/// this panel for its content.
class SignInPanel extends ConsumerStatefulWidget {
  const SignInPanel({super.key, required this.message, this.registerUrl});

  /// Why signing in is worth it, on this page.
  final String message;

  /// The website's sign-up page, for "Create an account".
  final Uri? registerUrl;

  @override
  ConsumerState<SignInPanel> createState() => _SignInPanelState();
}

class _SignInPanelState extends ConsumerState<SignInPanel> {
  final _email = TextEditingController();
  final _password = TextEditingController();
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _email.dispose();
    _password.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (_busy) return;
    final email = _email.text.trim();
    final password = _password.text;
    if (email.isEmpty || password.isEmpty) {
      setState(() => _error = 'Enter your email and password.');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await signInToAccount(ref, email, password);
    } catch (e) {
      if (mounted) setState(() => _error = accountErrorText(e));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return GlassSurface(
      blur: false,
      radius: GC.rPanel,
      padding: const EdgeInsets.all(24),
      child: AutofillGroup(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Container(
                  width: 38,
                  height: 38,
                  decoration: BoxDecoration(
                    color: GC.accent.withValues(alpha: 0.14),
                    borderRadius: BorderRadius.circular(GC.rInput),
                    border: Border.all(color: GC.borderAccent),
                  ),
                  child: const Icon(
                    Icons.login_rounded,
                    size: 18,
                    color: GC.accentText,
                  ),
                ),
                const SizedBox(width: 14),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        'Sign in to gocoder.org',
                        style: theme.textTheme.titleMedium,
                      ),
                      const SizedBox(height: 2),
                      Text(widget.message, style: theme.textTheme.bodySmall),
                    ],
                  ),
                ),
              ],
            ),
            const SizedBox(height: 20),
            TextField(
              controller: _email,
              keyboardType: TextInputType.emailAddress,
              autofillHints: const [AutofillHints.email],
              decoration: const InputDecoration(labelText: 'Email'),
            ),
            const SizedBox(height: 12),
            TextField(
              controller: _password,
              obscureText: true,
              autofillHints: const [AutofillHints.password],
              decoration: const InputDecoration(labelText: 'Password'),
              onSubmitted: (_) => _submit(),
            ),
            if (_error != null) ...[
              const SizedBox(height: 14),
              ErrorPanel(message: _error!),
            ],
            const SizedBox(height: 18),
            Wrap(
              alignment: WrapAlignment.spaceBetween,
              crossAxisAlignment: WrapCrossAlignment.center,
              runSpacing: 8,
              children: [
                if (widget.registerUrl != null)
                  TextButton(
                    onPressed: () => openExternal(widget.registerUrl!),
                    child: const Text('Create an account'),
                  )
                else
                  const SizedBox.shrink(),
                FilledButton(
                  onPressed: _busy ? null : _submit,
                  child: _busy
                      ? const SizedBox.square(
                          dimension: 16,
                          child: CircularProgressIndicator(
                            strokeWidth: 2,
                            color: GC.accentInk,
                          ),
                        )
                      : const Text('Sign in'),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

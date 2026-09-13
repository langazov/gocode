import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/connection/controller.dart';
import '../../shared/widgets/glass.dart';

/// Home: a starting point. The session history lives in the sidebar.
class HomeScreen extends ConsumerWidget {
  const HomeScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final connection = ref.watch(connectionProvider);
    final settings = ref.watch(settingsProvider);
    final where = settings.mode == ConnectionMode.local
        ? settings.workingDirectory
        : (connection.baseUrl ?? settings.remoteUrl);

    return Scaffold(
      extendBodyBehindAppBar: true,
      appBar: const PillHeader(),
      body: Center(
        child: SingleChildScrollView(
          padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 96),
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 560),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                const Eyebrow('Ready when you are'),
                const SizedBox(height: 24),
                Text(
                  'What are we building today?',
                  textAlign: TextAlign.center,
                  style: theme.textTheme.headlineMedium,
                ),
                if (where.isNotEmpty) ...[
                  const SizedBox(height: 10),
                  Text(
                    where,
                    textAlign: TextAlign.center,
                    style: GC.code.copyWith(color: GC.textFaint),
                  ),
                ],
                const SizedBox(height: 12),
                Text(
                  'Pick up a session from the history, or start a new one.',
                  textAlign: TextAlign.center,
                  style: theme.textTheme.bodyMedium,
                ),
                const SizedBox(height: 28),
                FilledButton.icon(
                  onPressed: () => context.go('/new'),
                  icon: const Icon(Icons.add_rounded, size: 18),
                  label: const Text('Start a session'),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

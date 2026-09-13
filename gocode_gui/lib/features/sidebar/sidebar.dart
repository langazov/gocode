import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import '../../core/connection/controller.dart';
import '../../shared/widgets/glass.dart';
import '../account/avatar.dart';
import '../account/providers.dart';
import '../home/providers.dart';
import 'history.dart';

/// Width of the docked sidebar (and the drawer on narrow windows).
const sidebarWidth = 284.0;

/// Keys tests reach sidebar controls by.
abstract final class SidebarKeys {
  static const newSession = ValueKey('sidebar-new-session');
  static const accountMenu = ValueKey('sidebar-account-menu');
}

/// The left navigation: new session, the session history grouped by when
/// each was last active, and the account menu at the bottom.
class Sidebar extends ConsumerStatefulWidget {
  const Sidebar({
    super.key,
    required this.location,
    this.onHide,
    this.onNavigate,
  });

  /// The current route path, for highlighting the open session.
  final String location;

  /// Hides the docked sidebar (wide windows only).
  final VoidCallback? onHide;

  /// Called before navigating, e.g. to close the drawer.
  final VoidCallback? onNavigate;

  @override
  ConsumerState<Sidebar> createState() => _SidebarState();
}

class _SidebarState extends ConsumerState<Sidebar> {
  final _search = TextEditingController();
  String _query = '';

  @override
  void didUpdateWidget(Sidebar oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.location != widget.location) {
      // Moving between pages is when titles change and sessions appear, so
      // refetch then rather than poll. After the frame: a provider can't be
      // invalidated while the tree is building.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) ref.invalidate(sessionsProvider);
      });
    }
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  String? get _activeSessionID {
    const prefix = '/session/';
    final location = widget.location;
    return location.startsWith(prefix)
        ? location.substring(prefix.length)
        : null;
  }

  void _go(String path) {
    widget.onNavigate?.call();
    context.go(path);
  }

  Future<void> _act(String action, Session session) async {
    final client = ref.read(apiClientProvider);
    if (client == null) return;
    try {
      switch (action) {
        case 'rename':
          final title = await _promptTitle(session.title);
          if (title == null || title.trim().isEmpty) return;
          await client.renameSession(session.id, title.trim());
        case 'fork':
          await client.forkSession(session.id);
        case 'delete':
          if (await _confirmDelete(session.title) != true) return;
          await client.deleteSession(session.id);
          if (mounted && _activeSessionID == session.id) context.go('/');
      }
      ref.invalidate(sessionsProvider);
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }

  Future<String?> _promptTitle(String current) {
    final controller = TextEditingController(text: current);
    return showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Rename session'),
        content: SizedBox(
          width: 420,
          child: TextField(
            controller: controller,
            autofocus: true,
            onSubmitted: (value) => Navigator.pop(context, value),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, controller.text),
            child: const Text('Save'),
          ),
        ],
      ),
    );
  }

  Future<bool?> _confirmDelete(String title) {
    return showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Delete session?'),
        content: Text(
          '"${title.isEmpty ? 'Untitled' : title}" will be removed permanently.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            style: FilledButton.styleFrom(
              backgroundColor: GC.down,
              foregroundColor: GC.textHi,
            ),
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Delete'),
          ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final sessions = ref.watch(sessionsProvider);
    // Its own Material: docked, the sidebar sits in a plain Row with no
    // Scaffold above it, and its text field, rows and menus all need one.
    return Material(
      color: GC.surface1,
      shape: const Border(right: BorderSide(color: GC.border)),
      child: SafeArea(
        right: false,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(18, 14, 6, 10),
              child: Row(
                children: [
                  const GocodeLogo(size: 20),
                  const Spacer(),
                  IconButton(
                    tooltip: 'Refresh',
                    visualDensity: VisualDensity.compact,
                    icon: const Icon(Icons.refresh_rounded, size: 18),
                    onPressed: () => ref.invalidate(sessionsProvider),
                  ),
                  if (widget.onHide != null)
                    IconButton(
                      tooltip: 'Hide sidebar',
                      visualDensity: VisualDensity.compact,
                      icon: const Icon(
                        Icons.keyboard_double_arrow_left_rounded,
                        size: 18,
                      ),
                      onPressed: widget.onHide,
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12),
              child: FilledButton.icon(
                key: SidebarKeys.newSession,
                onPressed: () => _go('/new'),
                icon: const Icon(Icons.add_rounded, size: 18),
                label: const Text('New session'),
              ),
            ),
            const SizedBox(height: 10),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12),
              child: TextField(
                controller: _search,
                onChanged: (v) =>
                    setState(() => _query = v.trim().toLowerCase()),
                style: const TextStyle(
                  fontFamily: GC.sans,
                  fontSize: 13.5,
                  color: GC.textHi,
                ),
                decoration: const InputDecoration(
                  hintText: 'Search sessions',
                  prefixIcon: Icon(Icons.search, size: 16),
                  contentPadding: EdgeInsets.symmetric(
                    horizontal: 12,
                    vertical: 10,
                  ),
                ),
              ),
            ),
            const SizedBox(height: 6),
            Expanded(
              child: sessions.when(
                loading: () => const Center(
                  child: SizedBox.square(
                    dimension: 18,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                ),
                error: (e, _) => Padding(
                  padding: const EdgeInsets.all(12),
                  child: ErrorPanel(
                    message: '$e',
                    onRetry: () => ref.invalidate(sessionsProvider),
                  ),
                ),
                data: (list) => _History(
                  sessions: list,
                  query: _query,
                  activeID: _activeSessionID,
                  onOpen: (session) => _go('/session/${session.id}'),
                  onAction: _act,
                ),
              ),
            ),
            const Divider(),
            _AccountButton(onNavigate: _go),
          ],
        ),
      ),
    );
  }
}

class _History extends StatelessWidget {
  const _History({
    required this.sessions,
    required this.query,
    required this.activeID,
    required this.onOpen,
    required this.onAction,
  });

  final List<Session> sessions;
  final String query;
  final String? activeID;
  final ValueChanged<Session> onOpen;
  final Future<void> Function(String action, Session session) onAction;

  @override
  Widget build(BuildContext context) {
    final now = DateTime.now();
    // Subagent sessions are children of another; they'd crowd the history.
    final visible =
        sessions
            .where(
              (s) =>
                  !s.isSubagent &&
                  (query.isEmpty ||
                      s.title.toLowerCase().contains(query) ||
                      s.directory.toLowerCase().contains(query)),
            )
            .toList()
          ..sort((a, b) => b.timeUpdated.compareTo(a.timeUpdated));
    if (visible.isEmpty) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(20, 24, 20, 0),
        child: Text(
          query.isEmpty ? 'No sessions yet.' : 'No sessions match.',
          textAlign: TextAlign.center,
          style: Theme.of(context).textTheme.bodySmall,
        ),
      );
    }
    return ListView(
      padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
      children: [
        for (final group in groupSessions(visible, now)) ...[
          Padding(
            padding: const EdgeInsets.fromLTRB(10, 14, 10, 6),
            child: Caption(group.label),
          ),
          for (final session in group.sessions)
            _SessionItem(
              session: session,
              active: session.id == activeID,
              age: shortAge(session.timeUpdated, now),
              onOpen: () => onOpen(session),
              onAction: (action) => onAction(action, session),
            ),
        ],
      ],
    );
  }
}

class _SessionItem extends StatefulWidget {
  const _SessionItem({
    required this.session,
    required this.active,
    required this.age,
    required this.onOpen,
    required this.onAction,
  });

  final Session session;
  final bool active;
  final String age;
  final VoidCallback onOpen;
  final ValueChanged<String> onAction;

  @override
  State<_SessionItem> createState() => _SessionItemState();
}

class _SessionItemState extends State<_SessionItem> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    final active = widget.active;
    final title = widget.session.title.isEmpty
        ? 'Untitled'
        : widget.session.title;
    return MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: Tooltip(
        message: widget.session.directory,
        waitDuration: const Duration(milliseconds: 700),
        child: Material(
          type: MaterialType.transparency,
          child: InkWell(
            onTap: widget.onOpen,
            borderRadius: BorderRadius.circular(10),
            child: Ink(
              decoration: BoxDecoration(
                color: active ? const Color(0x17FFFFFF) : null,
                borderRadius: BorderRadius.circular(10),
              ),
              padding: const EdgeInsets.fromLTRB(10, 0, 2, 0),
              child: SizedBox(
                height: 36,
                child: Row(
                  children: [
                    if (active)
                      Container(
                        width: 3,
                        height: 14,
                        margin: const EdgeInsets.only(right: 8),
                        decoration: BoxDecoration(
                          color: GC.accent,
                          borderRadius: BorderRadius.circular(2),
                        ),
                      ),
                    Expanded(
                      child: Text(
                        title,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                          fontFamily: GC.sans,
                          fontSize: 13.5,
                          fontWeight: active
                              ? FontWeight.w600
                              : FontWeight.w400,
                          color: active ? GC.textHi : GC.textBody,
                        ),
                      ),
                    ),
                    if (_hovered || active)
                      PopupMenuButton<String>(
                        tooltip: 'Session actions',
                        onSelected: widget.onAction,
                        itemBuilder: (_) => const [
                          PopupMenuItem(value: 'rename', child: Text('Rename')),
                          PopupMenuItem(value: 'fork', child: Text('Fork')),
                          PopupMenuItem(value: 'delete', child: Text('Delete')),
                        ],
                        child: const Padding(
                          padding: EdgeInsets.symmetric(
                            horizontal: 8,
                            vertical: 6,
                          ),
                          child: Icon(
                            Icons.more_horiz_rounded,
                            size: 16,
                            color: GC.textDim,
                          ),
                        ),
                      )
                    else
                      Padding(
                        padding: const EdgeInsets.only(right: 10),
                        child: Text(
                          widget.age,
                          style: const TextStyle(
                            fontFamily: GC.sans,
                            fontSize: 11.5,
                            color: GC.textFaint,
                          ),
                        ),
                      ),
                  ],
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// The account button at the bottom: who is signed in, and the menu for the
/// account pages, the app's settings, and signing in or out.
class _AccountButton extends ConsumerWidget {
  const _AccountButton({required this.onNavigate});

  final ValueChanged<String> onNavigate;

  Future<void> _signOut(BuildContext context, WidgetRef ref) async {
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
      if (context.mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(accountErrorText(e))));
      }
    }
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final account = ref.watch(accountProvider);
    final info = account.value;
    final signedIn = info?.signedIn ?? false;
    final expired = info?.expired ?? false;

    PopupMenuItem<String> item(String value, IconData icon, String label) =>
        PopupMenuItem<String>(
          value: value,
          height: 40,
          child: Row(
            children: [
              Icon(icon, size: 18, color: GC.textDim),
              const SizedBox(width: 12),
              Text(label),
            ],
          ),
        );

    final String title;
    final String subtitle;
    if (signedIn) {
      title = info!.name;
      subtitle = expired ? 'Sign in again' : info.email;
    } else {
      title = account.isLoading ? '…' : 'Not signed in';
      subtitle = 'Sign in to gocoder.org';
    }

    return Padding(
      padding: const EdgeInsets.all(8),
      child: PopupMenuButton<String>(
        key: SidebarKeys.accountMenu,
        tooltip: 'Account and settings',
        position: PopupMenuPosition.over,
        constraints: const BoxConstraints(minWidth: sidebarWidth - 16),
        onSelected: (value) {
          if (value == 'signout') {
            _signOut(context, ref);
          } else {
            onNavigate(value);
          }
        },
        itemBuilder: (_) => [
          if (signedIn)
            PopupMenuItem<String>(
              enabled: false,
              height: 34,
              child: Text(info!.email, style: theme.textTheme.bodySmall),
            ),
          item('/account/profile', Icons.person_outline_rounded, 'Profile'),
          item(
            '/account/settings',
            Icons.manage_accounts_outlined,
            'User settings',
          ),
          item('/settings', Icons.tune_rounded, 'Settings'),
          item('/account/usage', Icons.insights_rounded, 'Usage'),
          item(
            '/account/invite',
            Icons.card_giftcard_rounded,
            'Invite a friend',
          ),
          const PopupMenuDivider(),
          if (signedIn)
            item('signout', Icons.logout_rounded, 'Sign out')
          else
            item('/account/profile', Icons.login_rounded, 'Sign in'),
        ],
        child: Padding(
          padding: const EdgeInsets.all(8),
          child: Row(
            children: [
              AccountAvatar(
                initials: signedIn ? info!.initials : null,
                badge: expired ? GC.warn : null,
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall,
                    ),
                    Text(
                      subtitle,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
              const Icon(
                Icons.unfold_more_rounded,
                size: 18,
                color: GC.textDim,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

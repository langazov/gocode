import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../app/theme.dart';
import '../../core/api/account.dart';
import '../../shared/widgets/glass.dart';
import '../../shared/widgets/page_frame.dart';
import 'account_widgets.dart';
import 'daily_bars.dart';
import 'providers.dart';
import 'sign_in_panel.dart';

/// What the gocoder.org account has used: headline totals, tokens per day,
/// and the breakdown by model.
class UsageScreen extends ConsumerStatefulWidget {
  const UsageScreen({super.key});

  @override
  ConsumerState<UsageScreen> createState() => _UsageScreenState();
}

class _UsageScreenState extends ConsumerState<UsageScreen> {
  int _days = 30;

  @override
  Widget build(BuildContext context) {
    final account = ref.watch(accountProvider);
    final signedIn = account.value?.signedIn ?? false;
    final expired = account.value?.expired ?? false;
    return PageFrame(
      caption: 'Account',
      title: 'Usage',
      lead: 'Requests made through your gocoder.org account.',
      maxWidth: 880,
      children: [
        if (account.isLoading && account.value == null)
          const _Spinner()
        else if (!signedIn || expired)
          SignInPanel(
            message: 'See what your account has used.',
            registerUrl: account.value?.page('/register'),
          )
        else ...[
          Align(
            alignment: Alignment.centerLeft,
            child: SegmentedButton<int>(
              segments: const [
                ButtonSegment(value: 7, label: Text('7 days')),
                ButtonSegment(value: 30, label: Text('30 days')),
                ButtonSegment(value: 90, label: Text('90 days')),
              ],
              selected: {_days},
              showSelectedIcon: false,
              onSelectionChanged: (s) => setState(() => _days = s.first),
            ),
          ),
          const SizedBox(height: 16),
          ref
              .watch(usageProvider(_days))
              .when(
                loading: () => const _Spinner(),
                error: (e, _) => ErrorPanel(
                  message: accountErrorText(e),
                  onRetry: () => ref.invalidate(usageProvider(_days)),
                ),
                data: (summary) => _Usage(summary: summary, days: _days),
              ),
        ],
      ],
    );
  }
}

class _Spinner extends StatelessWidget {
  const _Spinner();

  @override
  Widget build(BuildContext context) => const Center(
    child: Padding(
      padding: EdgeInsets.all(32),
      child: CircularProgressIndicator(strokeWidth: 2),
    ),
  );
}

class _Usage extends StatelessWidget {
  const _Usage({required this.summary, required this.days});

  final UsageSummary summary;
  final int days;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final totals = summary.totals;
    final hitRate = totals.cacheHitRate;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Wrap(
          spacing: 12,
          runSpacing: 12,
          children: [
            StatTile(label: 'Requests', value: groupedNumber(totals.requests)),
            StatTile(
              label: 'Tokens',
              value: compactNumber(totals.tokens),
              detail: '${groupedNumber(totals.tokens)} total',
            ),
            StatTile(
              label: 'Cache hit rate',
              value: hitRate == null
                  ? '—'
                  : '${(hitRate * 100).toStringAsFixed(0)}%',
            ),
            StatTile(label: 'Errors', value: groupedNumber(totals.errors)),
            StatTile(
              label: 'Avg latency',
              value: totals.requests == 0
                  ? '—'
                  : '${totals.avgLatencyMs.round()} ms',
            ),
          ],
        ),
        const SizedBox(height: 20),
        GlassSurface(
          blur: false,
          radius: GC.rCard,
          padding: const EdgeInsets.all(20),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text('Tokens per day', style: theme.textTheme.titleMedium),
              const SizedBox(height: 2),
              Text(
                'Last $days days, by UTC day',
                style: theme.textTheme.bodySmall,
              ),
              const SizedBox(height: 18),
              SizedBox(height: 190, child: DailyBars(days: summary.daily)),
            ],
          ),
        ),
        const SizedBox(height: 20),
        AccountSection(
          title: 'By model',
          child: summary.byModel.isEmpty
              ? Text(
                  'No requests in this period.',
                  style: theme.textTheme.bodyMedium,
                )
              : _ModelTable(rows: summary.byModel),
        ),
      ],
    );
  }
}

class _ModelTable extends StatelessWidget {
  const _ModelTable({required this.rows});

  final List<UsageModel> rows;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final sorted = [...rows]..sort((a, b) => b.tokens.compareTo(a.tokens));
    const numeric = TextStyle(
      fontFamily: GC.sans,
      fontSize: 13.5,
      color: GC.textHi,
      fontFeatures: [FontFeature.tabularFigures()],
    );
    TableRow row(List<Widget> cells) => TableRow(
      children: [
        for (final cell in cells)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 8),
            child: cell,
          ),
      ],
    );
    return Table(
      columnWidths: const {
        0: FlexColumnWidth(3),
        1: FlexColumnWidth(1.4),
        2: FlexColumnWidth(1.4),
      },
      border: const TableBorder(horizontalInside: BorderSide(color: GC.border)),
      children: [
        row(const [
          Caption('Model'),
          Align(alignment: Alignment.centerRight, child: Caption('Requests')),
          Align(alignment: Alignment.centerRight, child: Caption('Tokens')),
        ]),
        for (final model in sorted.take(12))
          row([
            Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  model.model,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: GC.code.copyWith(color: GC.textHi),
                ),
                Text(model.provider, style: theme.textTheme.bodySmall),
              ],
            ),
            Text(
              groupedNumber(model.requests),
              textAlign: TextAlign.right,
              style: numeric,
            ),
            Text(
              groupedNumber(model.tokens),
              textAlign: TextAlign.right,
              style: numeric,
            ),
          ]),
      ],
    );
  }
}

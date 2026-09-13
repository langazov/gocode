import 'package:flutter/material.dart';

import '../../app/theme.dart';
import '../../core/api/models.dart';
import 'glass.dart';

/// The outcome of [showModelPicker]; a null [model] means "server default".
class ModelChoice {
  const ModelChoice(this.model);

  final ModelEntry? model;
}

/// A searchable model list grouped by provider. Returns null on cancel.
Future<ModelChoice?> showModelPicker(
  BuildContext context, {
  required List<ModelEntry> models,
  String? selectedKey,
  bool allowDefault = false,
}) => showDialog<ModelChoice>(
  context: context,
  barrierColor: const Color(0x99000000),
  builder: (_) => _ModelPickerDialog(
    models: models,
    selectedKey: selectedKey,
    allowDefault: allowDefault,
  ),
);

class _ModelPickerDialog extends StatefulWidget {
  const _ModelPickerDialog({
    required this.models,
    required this.selectedKey,
    required this.allowDefault,
  });

  final List<ModelEntry> models;
  final String? selectedKey;
  final bool allowDefault;

  @override
  State<_ModelPickerDialog> createState() => _ModelPickerDialogState();
}

class _ModelPickerDialogState extends State<_ModelPickerDialog> {
  String _query = '';

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final q = _query.toLowerCase();
    final filtered =
        widget.models
            .where(
              (m) =>
                  q.isEmpty ||
                  m.name.toLowerCase().contains(q) ||
                  m.id.toLowerCase().contains(q) ||
                  m.providerID.toLowerCase().contains(q),
            )
            .toList()
          ..sort((a, b) {
            final byProvider = a.providerID.compareTo(b.providerID);
            return byProvider != 0
                ? byProvider
                : a.name.toLowerCase().compareTo(b.name.toLowerCase());
          });

    final rows = <Widget>[
      if (widget.allowDefault && q.isEmpty)
        _ModelRow(
          title: 'Server default',
          subtitle: 'whatever the server is configured to use',
          selected: widget.selectedKey == null,
          onTap: () => Navigator.pop(context, const ModelChoice(null)),
        ),
    ];
    String? provider;
    for (final m in filtered) {
      if (m.providerID != provider) {
        provider = m.providerID;
        rows.add(
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 16, 12, 6),
            child: Caption(provider),
          ),
        );
      }
      rows.add(
        _ModelRow(
          title: m.name,
          subtitle: m.id,
          trailing: _contextLabel(m.contextLimit),
          selected: m.key == widget.selectedKey,
          onTap: () => Navigator.pop(context, ModelChoice(m)),
        ),
      );
    }

    return Dialog(
      backgroundColor: Colors.transparent,
      elevation: 0,
      insetPadding: const EdgeInsets.all(24),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 560, maxHeight: 640),
        child: GlassSurface(
          radius: GC.rPanel,
          tint: const Color(0xB31F1F1F),
          padding: const EdgeInsets.fromLTRB(20, 22, 20, 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text('Choose a model', style: theme.textTheme.titleLarge),
              const SizedBox(height: 4),
              Text(
                '${widget.models.length} available from connected providers',
                style: theme.textTheme.bodySmall,
              ),
              const SizedBox(height: 16),
              TextField(
                autofocus: true,
                onChanged: (v) => setState(() => _query = v.trim()),
                decoration: const InputDecoration(
                  hintText: 'Search models…',
                  prefixIcon: Icon(Icons.search, size: 18),
                ),
              ),
              const SizedBox(height: 8),
              Expanded(
                child: rows.isEmpty
                    ? Center(
                        child: Text(
                          widget.models.isEmpty
                              ? 'No models — connect a provider first'
                              : 'No models match "$_query"',
                          style: theme.textTheme.bodyMedium,
                        ),
                      )
                    : ListView(children: rows),
              ),
            ],
          ),
        ),
      ),
    );
  }

  static String? _contextLabel(int limit) {
    if (limit <= 0) return null;
    if (limit >= 1000000) {
      final m = limit / 1000000;
      return '${m == m.roundToDouble() ? m.round() : m.toStringAsFixed(1)}M';
    }
    return '${(limit / 1000).round()}k';
  }
}

class _ModelRow extends StatelessWidget {
  const _ModelRow({
    required this.title,
    required this.subtitle,
    required this.selected,
    required this.onTap,
    this.trailing,
  });

  final String title;
  final String subtitle;
  final String? trailing;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Material(
      type: MaterialType.transparency,
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(GC.rInput),
        child: Ink(
          decoration: BoxDecoration(
            color: selected ? const Color(0x1FE8862D) : null,
            borderRadius: BorderRadius.circular(GC.rInput),
          ),
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 9),
          child: Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall?.copyWith(
                        color: selected ? GC.accentText : GC.textHi,
                      ),
                    ),
                    Text(
                      subtitle,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: GC.code.copyWith(
                        fontSize: 11.5,
                        color: GC.textFaint,
                      ),
                    ),
                  ],
                ),
              ),
              if (trailing != null)
                Padding(
                  padding: const EdgeInsets.only(left: 12),
                  child: Text(trailing!, style: theme.textTheme.bodySmall),
                ),
              if (selected)
                const Padding(
                  padding: EdgeInsets.only(left: 10),
                  child: Icon(Icons.check_rounded, size: 18, color: GC.accent),
                ),
            ],
          ),
        ),
      ),
    );
  }
}

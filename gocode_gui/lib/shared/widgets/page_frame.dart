import 'package:flutter/material.dart';

import 'glass.dart';

/// A titled page under the floating header: caption, heading, optional lead
/// text, then [children] in a centered reading column.
class PageFrame extends StatelessWidget {
  const PageFrame({
    super.key,
    required this.caption,
    required this.title,
    this.lead,
    required this.children,
    this.maxWidth = 720,
  });

  final String caption;
  final String title;
  final String? lead;
  final List<Widget> children;
  final double maxWidth;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      extendBodyBehindAppBar: true,
      appBar: PillHeader(title: Text(title, style: theme.textTheme.titleSmall)),
      // Built inside the Scaffold, so the insets include the header.
      body: Builder(
        builder: (context) => ListView(
          padding: EdgeInsets.fromLTRB(
            16,
            MediaQuery.paddingOf(context).top + 28,
            16,
            48,
          ),
          children: [
            Align(
              alignment: Alignment.topCenter,
              child: ConstrainedBox(
                constraints: BoxConstraints(maxWidth: maxWidth),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    Caption(caption),
                    const SizedBox(height: 6),
                    Text(title, style: theme.textTheme.headlineMedium),
                    if (lead != null) ...[
                      const SizedBox(height: 8),
                      Text(lead!, style: theme.textTheme.bodyMedium),
                    ],
                    const SizedBox(height: 24),
                    ...children,
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

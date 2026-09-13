import 'dart:math' as math;
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

import '../../app/theme.dart';

/// The page ground: near-black with soft gray light pooled at the
/// edges — something for the glass surfaces above it to refract.
class AmbientBackground extends StatelessWidget {
  const AmbientBackground({super.key, required this.child});

  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Stack(
      fit: StackFit.expand,
      children: [
        const RepaintBoundary(child: CustomPaint(painter: _AmbientPainter())),
        child,
      ],
    );
  }
}

class _AmbientPainter extends CustomPainter {
  const _AmbientPainter();

  @override
  void paint(Canvas canvas, Size size) {
    final rect = Offset.zero & size;
    canvas.drawRect(rect, Paint()..color = GC.bgPage);
    final extent = size.longestSide;

    void glow(Offset center, double radius, Color color) {
      canvas.drawRect(
        rect,
        Paint()
          ..shader = ui.Gradient.radial(
            center,
            radius,
            [
              color,
              color.withValues(alpha: color.a * 0.35),
              color.withValues(alpha: 0),
            ],
            const [0, 0.45, 1],
          ),
      );
    }

    glow(
      Offset(size.width * 0.12, -size.height * 0.08),
      extent * 0.62,
      const Color(0xFFFFFFFF).withValues(alpha: 0.07),
    );
    glow(
      Offset(size.width * 1.02, size.height * 0.42),
      extent * 0.48,
      const Color(0xFFFFFFFF).withValues(alpha: 0.045),
    );
    glow(
      Offset(size.width * 0.38, size.height * 1.08),
      extent * 0.55,
      const Color(0xFFFFFFFF).withValues(alpha: 0.04),
    );
  }

  @override
  bool shouldRepaint(_AmbientPainter oldDelegate) => false;
}

/// `backdrop-filter: blur(24px) saturate(180%)`, the site's glass filter.
final ui.ImageFilter _glassFilter = ui.ImageFilter.compose(
  outer: ui.ImageFilter.blur(sigmaX: 24, sigmaY: 24),
  inner: const ColorFilter.matrix(<double>[
    1.62992, -0.57216, -0.05776, 0, 0, //
    -0.17008, 1.22784, -0.05776, 0, 0, //
    -0.17008, -0.57216, 1.74224, 0, 0, //
    0, 0, 0, 1, 0, //
  ]),
);

/// A liquid-glass panel: frosted backdrop, a faint white sheen from the top
/// left, a specular gradient rim, and a soft drop shadow. With [onTap] it
/// lifts on hover and gives slightly when pressed.
class GlassSurface extends StatefulWidget {
  const GlassSurface({
    super.key,
    required this.child,
    this.radius = GC.rCard,
    this.padding = EdgeInsets.zero,
    this.blur = true,
    this.shadow = true,
    this.tint,
    this.borderColor,
    this.onTap,
  });

  final Widget child;
  final double radius;
  final EdgeInsetsGeometry padding;

  /// Frost what's behind. Worth it only where content moves underneath
  /// (headers, the composer, dialogs); over the static ground it's invisible
  /// and just costs a render pass.
  final bool blur;
  final bool shadow;

  /// Painted under the glass sheen, e.g. an accent wash or extra opacity.
  final Color? tint;
  final Color? borderColor;
  final VoidCallback? onTap;

  @override
  State<GlassSurface> createState() => _GlassSurfaceState();
}

class _GlassSurfaceState extends State<GlassSurface> {
  bool _hovered = false;
  bool _pressed = false;

  @override
  Widget build(BuildContext context) {
    final radius = BorderRadius.circular(widget.radius);
    Widget surface = CustomPaint(
      painter: _GlassFillPainter(tint: widget.tint),
      foregroundPainter: _GlassRimPainter(
        radius: widget.radius,
        border:
            widget.borderColor ?? (_hovered ? GC.borderStrong : GC.glassBorder),
      ),
      child: Padding(padding: widget.padding, child: widget.child),
    );
    if (widget.blur) {
      surface = BackdropFilter(filter: _glassFilter, child: surface);
    }
    surface = ClipRRect(borderRadius: radius, child: surface);
    if (widget.shadow) {
      surface = DecoratedBox(
        decoration: BoxDecoration(
          borderRadius: radius,
          boxShadow: GC.cardShadow,
        ),
        child: surface,
      );
    }
    if (widget.onTap == null) return surface;

    final lift = _hovered && !_pressed ? -2.0 : 0.0;
    final scale = _pressed ? 0.985 : 1.0;
    return Semantics(
      button: true,
      child: MouseRegion(
        cursor: SystemMouseCursors.click,
        onEnter: (_) => setState(() => _hovered = true),
        onExit: (_) => setState(() {
          _hovered = false;
          _pressed = false;
        }),
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTapDown: (_) => setState(() => _pressed = true),
          onTapUp: (_) => setState(() => _pressed = false),
          onTapCancel: () => setState(() => _pressed = false),
          onTap: widget.onTap,
          child: AnimatedContainer(
            duration: GC.dur,
            curve: GC.ease,
            transformAlignment: Alignment.center,
            transform: Matrix4.translationValues(0, lift, 0)
              ..multiply(Matrix4.diagonal3Values(scale, scale, 1)),
            child: surface,
          ),
        ),
      ),
    );
  }
}

class _GlassFillPainter extends CustomPainter {
  const _GlassFillPainter({this.tint});

  final Color? tint;

  @override
  void paint(Canvas canvas, Size size) {
    if (size.isEmpty) return;
    final rect = Offset.zero & size;
    if (tint != null) canvas.drawRect(rect, Paint()..color = tint!);

    // linear-gradient(160deg, #ffffff12, #ffffff06 45%, #ffffff0b)
    canvas.drawRect(
      rect,
      Paint()
        ..shader = const LinearGradient(
          begin: Alignment(-0.34, -0.94),
          end: Alignment(0.34, 0.94),
          colors: [Color(0x12FFFFFF), Color(0x06FFFFFF), Color(0x0BFFFFFF)],
          stops: [0, 0.45, 1],
        ).createShader(rect),
    );

    // radial-gradient(140% 90% at 15% -20%, #ffffff1a, transparent 55%):
    // draw a unit-circle gradient in a space scaled to the ellipse.
    canvas
      ..save()
      ..translate(size.width * 0.15, -size.height * 0.2)
      ..scale(size.width * 1.4, size.height * 0.9)
      ..drawRect(
        const Rect.fromLTRB(-1, -1, 1, 2),
        Paint()
          ..shader = ui.Gradient.radial(
            Offset.zero,
            1,
            const [Color(0x1AFFFFFF), Color(0x00FFFFFF)],
            const [0, 0.55],
          ),
      )
      ..restore();
  }

  @override
  bool shouldRepaint(_GlassFillPainter oldDelegate) => oldDelegate.tint != tint;
}

class _GlassRimPainter extends CustomPainter {
  const _GlassRimPainter({required this.radius, required this.border});

  final double radius;
  final Color border;

  @override
  void paint(Canvas canvas, Size size) {
    if (size.isEmpty) return;
    final rect = Offset.zero & size;
    final r = math.min(radius, size.shortestSide / 2);
    final outline = RRect.fromRectAndRadius(
      rect.deflate(0.5),
      Radius.circular(math.max(0, r - 0.5)),
    );

    // Flat hairline, then the specular rim over it.
    canvas.drawRRect(
      outline,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1
        ..color = border,
    );
    canvas.drawRRect(
      outline,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1
        ..shader = const LinearGradient(
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
          colors: [
            Color(0x61FFFFFF),
            Color(0x0FFFFFFF),
            Color(0x05FFFFFF),
            Color(0x33FFFFFF),
          ],
          stops: [0, 0.35, 0.65, 1],
        ).createShader(rect),
    );

    // inset 0 1px 0 #ffffff24 — light catching the top edge.
    canvas
      ..save()
      ..clipRRect(RRect.fromRectAndRadius(rect, Radius.circular(r)))
      ..drawRect(
        Rect.fromLTWH(0, 1, size.width, 1),
        Paint()..color = const Color(0x24FFFFFF),
      )
      ..restore();
  }

  @override
  bool shouldRepaint(_GlassRimPainter oldDelegate) =>
      oldDelegate.radius != radius || oldDelegate.border != border;
}

/// The wordmark: `gocode_` in Space Grotesk with an accent cursor.
class GocodeLogo extends StatelessWidget {
  const GocodeLogo({super.key, this.size = 22});

  final double size;

  @override
  Widget build(BuildContext context) {
    return Text.rich(
      TextSpan(
        text: 'gocode',
        children: const [
          TextSpan(
            text: '_',
            style: TextStyle(color: GC.accent),
          ),
        ],
      ),
      maxLines: 1,
      style: TextStyle(
        fontFamily: GC.display,
        fontSize: size,
        fontWeight: FontWeight.w700,
        letterSpacing: -0.02 * size,
        height: 1.1,
        color: GC.textHi,
      ),
    );
  }
}

/// The floating glass pill the site uses for navigation, as an app bar.
///
/// Pair with `Scaffold(extendBodyBehindAppBar: true)` so content scrolls
/// under the frosted glass.
class PillHeader extends StatelessWidget implements PreferredSizeWidget {
  const PillHeader({
    super.key,
    this.leading,
    this.title,
    this.actions = const [],
  });

  final Widget? leading;
  final Widget? title;
  final List<Widget> actions;

  static const _height = 60.0;

  @override
  Size get preferredSize => const Size.fromHeight(_height + 20);

  @override
  Widget build(BuildContext context) {
    return SafeArea(
      bottom: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 12, 16, 8),
        child: Align(
          alignment: Alignment.topCenter,
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 1240),
            child: GlassSurface(
              radius: _height / 2,
              padding: EdgeInsets.only(
                left: leading == null ? 22 : 8,
                right: 8,
              ),
              child: SizedBox(
                height: _height,
                child: Row(
                  children: [
                    if (leading != null) ...[
                      leading!,
                      const SizedBox(width: 6),
                    ],
                    Expanded(
                      child: Align(
                        alignment: Alignment.centerLeft,
                        child: title ?? const GocodeLogo(),
                      ),
                    ),
                    ...actions,
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

/// Back when there's history, home otherwise (deep links, `go` replaces).
class HeaderBackButton extends StatelessWidget {
  const HeaderBackButton({super.key});

  @override
  Widget build(BuildContext context) {
    return IconButton(
      tooltip: 'Back',
      icon: const Icon(Icons.arrow_back_rounded),
      onPressed: () => context.canPop() ? context.pop() : context.go('/'),
    );
  }
}

/// A glass pill with spaced uppercase text (the site's `.eyebrow`).
class Eyebrow extends StatelessWidget {
  const Eyebrow(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) {
    return GlassSurface(
      radius: 999,
      shadow: false,
      padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
      child: Text(
        text.toUpperCase(),
        style: const TextStyle(
          fontFamily: GC.sans,
          fontSize: 12.5,
          fontWeight: FontWeight.w500,
          letterSpacing: 2.2,
          height: 1.2,
          color: Color(0xFFB3A89B),
        ),
      ),
    );
  }
}

/// Small uppercase section label.
class Caption extends StatelessWidget {
  const Caption(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) =>
      Text(text.toUpperCase(), style: GC.caption);
}

/// A bordered status pill: `WORKING`, `IDLE`, `FAILED`…
class StatusPill extends StatelessWidget {
  const StatusPill({super.key, required this.label, required this.color});

  final String label;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.10),
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: color.withValues(alpha: 0.45)),
      ),
      child: Text(
        label.toUpperCase(),
        style: TextStyle(
          fontFamily: GC.sans,
          fontSize: 11,
          fontWeight: FontWeight.w600,
          letterSpacing: 0.9,
          height: 1,
          color: color,
        ),
      ),
    );
  }
}

/// Selectable monospace text on a darkened well.
class CodeBlock extends StatelessWidget {
  const CodeBlock(this.text, {super.key, this.maxHeight, this.maxLines});

  final String text;
  final double? maxHeight;
  final int? maxLines;

  @override
  Widget build(BuildContext context) {
    Widget body = SelectableText(text, style: GC.code, maxLines: maxLines);
    if (maxHeight != null) {
      body = ConstrainedBox(
        constraints: BoxConstraints(maxHeight: maxHeight!),
        child: SingleChildScrollView(child: body),
      );
    }
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: const Color(0x40000000),
        borderRadius: BorderRadius.circular(10),
        border: Border.all(color: GC.border),
      ),
      child: body,
    );
  }
}

/// An inline failure: message, optional log tail, optional retry.
class ErrorPanel extends StatelessWidget {
  const ErrorPanel({
    super.key,
    required this.message,
    this.details = const [],
    this.onRetry,
  });

  final String message;
  final List<String> details;
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final tail = details.skip(math.max(0, details.length - 8)).join('\n');
    return Container(
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: GC.down.withValues(alpha: 0.08),
        borderRadius: BorderRadius.circular(GC.rInput),
        border: Border.all(color: GC.down.withValues(alpha: 0.4)),
      ),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Padding(
                padding: EdgeInsets.only(top: 2),
                child: Icon(Icons.error_outline, size: 18, color: GC.downText),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: SelectableText(
                  message,
                  style: theme.textTheme.bodyMedium?.copyWith(
                    color: GC.downText,
                  ),
                ),
              ),
            ],
          ),
          if (tail.isNotEmpty) ...[
            const SizedBox(height: 10),
            CodeBlock(tail, maxHeight: 160),
          ],
          if (onRetry != null) ...[
            const SizedBox(height: 6),
            Align(
              alignment: Alignment.centerRight,
              child: TextButton(onPressed: onRetry, child: const Text('Retry')),
            ),
          ],
        ],
      ),
    );
  }
}

import 'package:flutter/material.dart';

/// Design tokens after gocoder.org: a neutral near-black ground, one orange
/// accent, and translucent "liquid glass" surfaces. The site is dark-only,
/// and so is the app.
abstract final class GC {
  // Ground and solid surfaces.
  static const bgPage = Color(0xFF171717);
  static const surface1 = Color(0xFF1F1F1F);
  static const surface2 = Color(0xFF262626);
  static const surface3 = Color(0xFF2E2E2E);

  // Accent.
  static const accent = Color(0xFFE8862D);
  static const accentHover = Color(0xFFF39741);
  static const accentPress = Color(0xFFD2751F);
  static const accentText = Color(0xFFEFA35B);
  static const accentInk = Color(0xFF26160A);

  // Text.
  static const textHi = Color(0xFFF4EFE9);
  static const textBody = Color(0xFFB4A99E);
  static const textDim = Color(0xFF94897D);
  static const textFaint = Color(0xFF6E645A);

  // Lines.
  static const border = Color(0x14FFF0E0);
  static const borderStrong = Color(0x24FFF0E0);
  static const borderAccent = Color(0x61E8862D);
  static const glassBorder = Color(0x12FFFFFF);

  // Status.
  static const ok = Color(0xFF9CB878);
  static const warn = Color(0xFFE8A13C);
  static const down = Color(0xFFE06148);
  static const downText = Color(0xFFF08A76);

  // Radii.
  static const rInput = 12.0;
  static const rItem = 16.0;
  static const rCard = 20.0;
  static const rPanel = 26.0;

  // Motion.
  static const dur = Duration(milliseconds: 180);
  static const ease = Cubic(.2, .7, .3, 1);

  // Type families (bundled in assets/fonts).
  static const sans = 'DMSans';
  static const display = 'SpaceGrotesk';
  static const mono = 'JetBrainsMono';

  static const cardShadow = [
    BoxShadow(
      color: Color(0x8C000000),
      offset: Offset(0, 24),
      blurRadius: 48,
      spreadRadius: -24,
    ),
  ];

  /// Code, paths, tool output.
  static const code = TextStyle(
    fontFamily: mono,
    fontSize: 12.5,
    height: 1.5,
    color: textBody,
  );

  /// Small uppercase section label (the site's `.caption`).
  static const caption = TextStyle(
    fontFamily: sans,
    fontSize: 12,
    fontWeight: FontWeight.w500,
    letterSpacing: 1.2,
    color: textFaint,
  );
}

class AppTheme {
  static const _buttonText = TextStyle(
    fontFamily: GC.sans,
    fontSize: 15,
    fontWeight: FontWeight.w600,
  );

  static const _text = TextTheme(
    displaySmall: TextStyle(
      fontFamily: GC.display,
      fontSize: 40,
      fontWeight: FontWeight.w700,
      letterSpacing: -0.8,
      height: 1.05,
      color: GC.textHi,
    ),
    headlineMedium: TextStyle(
      fontFamily: GC.display,
      fontSize: 30,
      fontWeight: FontWeight.w700,
      letterSpacing: -0.5,
      height: 1.12,
      color: GC.textHi,
    ),
    headlineSmall: TextStyle(
      fontFamily: GC.display,
      fontSize: 24,
      fontWeight: FontWeight.w700,
      letterSpacing: -0.36,
      height: 1.12,
      color: GC.textHi,
    ),
    titleLarge: TextStyle(
      fontFamily: GC.display,
      fontSize: 20,
      fontWeight: FontWeight.w700,
      letterSpacing: -0.3,
      height: 1.2,
      color: GC.textHi,
    ),
    titleMedium: TextStyle(
      fontFamily: GC.sans,
      fontSize: 15.5,
      fontWeight: FontWeight.w600,
      height: 1.35,
      color: GC.textHi,
    ),
    titleSmall: TextStyle(
      fontFamily: GC.sans,
      fontSize: 14.5,
      fontWeight: FontWeight.w600,
      height: 1.35,
      color: GC.textHi,
    ),
    bodyLarge: TextStyle(
      fontFamily: GC.sans,
      fontSize: 15.5,
      height: 1.6,
      color: GC.textBody,
    ),
    bodyMedium: TextStyle(
      fontFamily: GC.sans,
      fontSize: 14.5,
      height: 1.6,
      color: GC.textBody,
    ),
    bodySmall: TextStyle(
      fontFamily: GC.sans,
      fontSize: 12.5,
      height: 1.45,
      color: GC.textDim,
    ),
    labelLarge: TextStyle(
      fontFamily: GC.sans,
      fontSize: 14.5,
      fontWeight: FontWeight.w600,
      color: GC.textHi,
    ),
    labelMedium: TextStyle(
      fontFamily: GC.sans,
      fontSize: 13,
      fontWeight: FontWeight.w500,
      color: GC.textBody,
    ),
    labelSmall: TextStyle(
      fontFamily: GC.sans,
      fontSize: 11.5,
      fontWeight: FontWeight.w500,
      letterSpacing: 0.2,
      color: GC.textDim,
    ),
  );

  static WidgetStateProperty<Color?> _resolve(
    Color base, {
    Color? hovered,
    Color? pressed,
    Color? disabled,
  }) => WidgetStateProperty.resolveWith((states) {
    if (states.contains(WidgetState.disabled)) return disabled ?? base;
    if (states.contains(WidgetState.pressed)) {
      return pressed ?? hovered ?? base;
    }
    if (states.contains(WidgetState.hovered)) return hovered ?? base;
    return base;
  });

  static OutlineInputBorder _inputBorder(Color color) => OutlineInputBorder(
    borderRadius: BorderRadius.circular(GC.rInput),
    borderSide: BorderSide(color: color),
  );

  static ThemeData dark() {
    const scheme = ColorScheme(
      brightness: Brightness.dark,
      primary: GC.accent,
      onPrimary: GC.accentInk,
      primaryContainer: Color(0x33E8862D),
      onPrimaryContainer: GC.accentText,
      secondary: GC.accentText,
      onSecondary: GC.accentInk,
      tertiary: GC.ok,
      onTertiary: GC.bgPage,
      error: GC.down,
      onError: GC.textHi,
      errorContainer: Color(0x29E06148),
      onErrorContainer: GC.downText,
      surface: GC.bgPage,
      onSurface: GC.textHi,
      onSurfaceVariant: GC.textBody,
      surfaceContainerLowest: Color(0xFF1A1A1A),
      surfaceContainerLow: GC.surface1,
      surfaceContainer: GC.surface1,
      surfaceContainerHigh: GC.surface2,
      surfaceContainerHighest: GC.surface3,
      outline: GC.textDim,
      outlineVariant: GC.borderStrong,
      shadow: Colors.black,
      scrim: Colors.black,
      inverseSurface: GC.textHi,
      onInverseSurface: GC.bgPage,
      inversePrimary: GC.accentPress,
      surfaceTint: Colors.transparent,
    );

    return ThemeData(
      useMaterial3: true,
      colorScheme: scheme,
      fontFamily: GC.sans,
      textTheme: _text,
      primaryTextTheme: _text,
      // Every route paints its own AmbientBackground under the glass.
      scaffoldBackgroundColor: Colors.transparent,
      canvasColor: GC.surface1,
      splashFactory: InkRipple.splashFactory,
      splashColor: const Color(0x14FFFFFF),
      highlightColor: Colors.transparent,
      hoverColor: const Color(0x0AFFFFFF),
      focusColor: const Color(0x29E8862D),
      appBarTheme: const AppBarTheme(
        backgroundColor: Colors.transparent,
        foregroundColor: GC.textHi,
        elevation: 0,
        scrolledUnderElevation: 0,
        surfaceTintColor: Colors.transparent,
      ),
      filledButtonTheme: FilledButtonThemeData(
        style: ButtonStyle(
          minimumSize: const WidgetStatePropertyAll(Size(64, 44)),
          padding: const WidgetStatePropertyAll(
            EdgeInsets.symmetric(horizontal: 26),
          ),
          shape: const WidgetStatePropertyAll(StadiumBorder()),
          elevation: const WidgetStatePropertyAll(0),
          textStyle: const WidgetStatePropertyAll(_buttonText),
          backgroundColor: _resolve(
            GC.accent,
            hovered: GC.accentHover,
            pressed: GC.accentPress,
            disabled: const Color(0x8CE8862D),
          ),
          foregroundColor: _resolve(
            GC.accentInk,
            disabled: const Color(0xCC26160A),
          ),
          iconColor: _resolve(GC.accentInk, disabled: const Color(0xCC26160A)),
          overlayColor: const WidgetStatePropertyAll(Colors.transparent),
        ),
      ),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style: ButtonStyle(
          minimumSize: const WidgetStatePropertyAll(Size(64, 44)),
          padding: const WidgetStatePropertyAll(
            EdgeInsets.symmetric(horizontal: 22),
          ),
          shape: const WidgetStatePropertyAll(StadiumBorder()),
          textStyle: const WidgetStatePropertyAll(_buttonText),
          backgroundColor: _resolve(
            const Color(0x0FFFFFFF),
            hovered: const Color(0x1AFFFFFF),
            disabled: const Color(0x08FFFFFF),
          ),
          foregroundColor: _resolve(GC.textHi, disabled: GC.textFaint),
          iconColor: _resolve(GC.textHi, disabled: GC.textFaint),
          overlayColor: const WidgetStatePropertyAll(Colors.transparent),
          side: WidgetStateProperty.resolveWith(
            (states) => BorderSide(
              color: states.contains(WidgetState.hovered)
                  ? const Color(0x3DFFFFFF)
                  : const Color(0x1FFFFFFF),
            ),
          ),
        ),
      ),
      textButtonTheme: TextButtonThemeData(
        style: ButtonStyle(
          shape: const WidgetStatePropertyAll(StadiumBorder()),
          padding: const WidgetStatePropertyAll(
            EdgeInsets.symmetric(horizontal: 16, vertical: 10),
          ),
          textStyle: const WidgetStatePropertyAll(
            TextStyle(
              fontFamily: GC.sans,
              fontSize: 14,
              fontWeight: FontWeight.w600,
            ),
          ),
          foregroundColor: _resolve(
            GC.accentText,
            hovered: GC.accentHover,
            disabled: GC.textFaint,
          ),
          overlayColor: const WidgetStatePropertyAll(Color(0x0FFFFFFF)),
        ),
      ),
      iconButtonTheme: IconButtonThemeData(
        style: ButtonStyle(
          foregroundColor: _resolve(
            GC.textBody,
            hovered: GC.textHi,
            disabled: GC.textFaint,
          ),
          overlayColor: const WidgetStatePropertyAll(Color(0x12FFFFFF)),
        ),
      ),
      segmentedButtonTheme: SegmentedButtonThemeData(
        style: ButtonStyle(
          shape: const WidgetStatePropertyAll(StadiumBorder()),
          side: const WidgetStatePropertyAll(
            BorderSide(color: GC.borderStrong),
          ),
          backgroundColor: WidgetStateProperty.resolveWith(
            (states) => states.contains(WidgetState.selected)
                ? const Color(0x29E8862D)
                : Colors.transparent,
          ),
          foregroundColor: WidgetStateProperty.resolveWith(
            (states) => states.contains(WidgetState.selected)
                ? GC.accentText
                : GC.textBody,
          ),
          iconColor: WidgetStateProperty.resolveWith(
            (states) => states.contains(WidgetState.selected)
                ? GC.accentText
                : GC.textDim,
          ),
          textStyle: const WidgetStatePropertyAll(
            TextStyle(
              fontFamily: GC.sans,
              fontSize: 14,
              fontWeight: FontWeight.w600,
            ),
          ),
          overlayColor: const WidgetStatePropertyAll(Color(0x0AFFFFFF)),
        ),
      ),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: const Color(0x0AFFFFFF),
        isDense: true,
        contentPadding: const EdgeInsets.symmetric(
          horizontal: 14,
          vertical: 13,
        ),
        hintStyle: const TextStyle(
          fontFamily: GC.sans,
          fontSize: 14,
          color: GC.textFaint,
        ),
        labelStyle: const TextStyle(
          fontFamily: GC.sans,
          fontSize: 14,
          color: GC.textDim,
        ),
        floatingLabelStyle: const TextStyle(
          fontFamily: GC.sans,
          color: GC.accentText,
        ),
        prefixIconColor: GC.textDim,
        suffixIconColor: GC.textDim,
        border: _inputBorder(GC.borderStrong),
        enabledBorder: _inputBorder(GC.borderStrong),
        focusedBorder: _inputBorder(const Color(0xB3E8862D)),
        errorBorder: _inputBorder(GC.down),
        focusedErrorBorder: _inputBorder(GC.down),
        disabledBorder: _inputBorder(GC.border),
      ),
      chipTheme: const ChipThemeData(
        backgroundColor: Color(0x0AFFFFFF),
        selectedColor: Color(0x29E8862D),
        side: BorderSide(color: GC.borderStrong),
        shape: StadiumBorder(),
        labelStyle: TextStyle(
          fontFamily: GC.sans,
          fontSize: 13.5,
          fontWeight: FontWeight.w500,
          color: GC.textBody,
        ),
        padding: EdgeInsets.symmetric(horizontal: 10, vertical: 6),
        showCheckmark: false,
      ),
      dialogTheme: DialogThemeData(
        backgroundColor: const Color(0xF21F1F1F),
        elevation: 0,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(GC.rPanel),
          side: const BorderSide(color: GC.borderStrong),
        ),
        titleTextStyle: _text.titleLarge,
        contentTextStyle: _text.bodyMedium,
      ),
      popupMenuTheme: PopupMenuThemeData(
        color: GC.surface2,
        elevation: 8,
        shadowColor: Colors.black,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(GC.rItem),
          side: const BorderSide(color: GC.borderStrong),
        ),
        textStyle: const TextStyle(
          fontFamily: GC.sans,
          fontSize: 14,
          color: GC.textHi,
        ),
      ),
      snackBarTheme: SnackBarThemeData(
        behavior: SnackBarBehavior.floating,
        backgroundColor: GC.surface3,
        elevation: 0,
        contentTextStyle: const TextStyle(
          fontFamily: GC.sans,
          fontSize: 14,
          color: GC.textHi,
        ),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(GC.rInput),
          side: const BorderSide(color: GC.borderStrong),
        ),
      ),
      bottomSheetTheme: const BottomSheetThemeData(
        backgroundColor: GC.surface1,
        modalBackgroundColor: GC.surface1,
        surfaceTintColor: Colors.transparent,
      ),
      progressIndicatorTheme: const ProgressIndicatorThemeData(
        color: GC.accent,
        linearTrackColor: GC.border,
        circularTrackColor: Colors.transparent,
      ),
      dividerTheme: const DividerThemeData(
        color: GC.border,
        thickness: 1,
        space: 1,
      ),
      tooltipTheme: TooltipThemeData(
        decoration: BoxDecoration(
          color: GC.surface3,
          borderRadius: BorderRadius.circular(8),
          border: Border.all(color: GC.borderStrong),
        ),
        textStyle: const TextStyle(
          fontFamily: GC.sans,
          fontSize: 12,
          color: GC.textHi,
        ),
      ),
      checkboxTheme: CheckboxThemeData(
        fillColor: WidgetStateProperty.resolveWith(
          (states) => states.contains(WidgetState.selected)
              ? GC.accent
              : Colors.transparent,
        ),
        checkColor: const WidgetStatePropertyAll(GC.accentInk),
        side: const BorderSide(color: GC.textDim, width: 1.5),
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(5)),
      ),
      listTileTheme: const ListTileThemeData(
        textColor: GC.textHi,
        iconColor: GC.textDim,
      ),
      expansionTileTheme: const ExpansionTileThemeData(
        iconColor: GC.textDim,
        collapsedIconColor: GC.textDim,
        shape: Border(),
        collapsedShape: Border(),
      ),
      textSelectionTheme: const TextSelectionThemeData(
        cursorColor: GC.accent,
        selectionColor: Color(0x59E8862D),
        selectionHandleColor: GC.accent,
      ),
      scrollbarTheme: const ScrollbarThemeData(
        thumbColor: WidgetStatePropertyAll(Color(0x33FFF0E0)),
        radius: Radius.circular(8),
        thickness: WidgetStatePropertyAll(6),
      ),
    );
  }
}

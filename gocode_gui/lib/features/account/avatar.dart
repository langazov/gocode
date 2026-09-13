import 'package:flutter/material.dart';

import '../../app/theme.dart';

/// A round avatar: initials on the website icon's ember gradient, or a
/// person glyph when nobody is signed in. [badge] adds a status dot.
class AccountAvatar extends StatelessWidget {
  const AccountAvatar({super.key, this.initials, this.size = 34, this.badge});

  final String? initials;
  final double size;
  final Color? badge;

  @override
  Widget build(BuildContext context) {
    final signedIn = initials != null;
    return SizedBox.square(
      dimension: size,
      child: Stack(
        clipBehavior: Clip.none,
        children: [
          Container(
            alignment: Alignment.center,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: signedIn ? null : const Color(0x14FFFFFF),
              gradient: signedIn
                  ? const LinearGradient(
                      begin: Alignment.topLeft,
                      end: Alignment.bottomRight,
                      colors: [Color(0xFFF6B26B), Color(0xFFE2792F)],
                    )
                  : null,
              border: Border.all(
                color: signedIn ? const Color(0x33FFFFFF) : GC.borderStrong,
              ),
            ),
            child: signedIn
                ? Text(
                    initials!,
                    style: TextStyle(
                      fontFamily: GC.display,
                      fontSize: size * 0.38,
                      fontWeight: FontWeight.w700,
                      height: 1,
                      color: GC.accentInk,
                    ),
                  )
                : Icon(
                    Icons.person_outline_rounded,
                    size: size * 0.52,
                    color: GC.textDim,
                  ),
          ),
          if (badge != null)
            Positioned(
              right: -1,
              bottom: -1,
              child: Container(
                width: size * 0.3,
                height: size * 0.3,
                decoration: BoxDecoration(
                  shape: BoxShape.circle,
                  color: badge,
                  border: Border.all(color: GC.surface1, width: 2),
                ),
              ),
            ),
        ],
      ),
    );
  }
}

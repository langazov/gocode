#!/usr/bin/env bash
#
# Regenerates every platform's app icon from assets/icon/app_icon.svg — the
# ">_" mark shared with the gocoder.org website. The outputs are committed, so
# this only needs running when the SVG changes.
#
# Needs rsvg-convert and ImageMagick:  brew install librsvg imagemagick
set -euo pipefail
cd "$(dirname "$0")/.."

src=assets/icon/app_icon.svg
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

render() { rsvg-convert -w "$2" -h "$2" "$1" -o "$3"; }

# ── macOS ──────────────────────────────────────────────────────────────────
# Apple's icon grid puts an 824px tile on a 1024px canvas with a soft shadow
# underneath. The favicon's near full-bleed tile would look oversized in the
# Dock next to every other app, so it is inset here.
render "$src" 824 "$tmp/tile.png"
magick -size 1024x1024 xc:none \
  \( "$tmp/tile.png" \( +clone -background black -shadow 45x14+0+10 \) \
     +swap -background none -layers merge +repage \) \
  -gravity center -composite "$tmp/macos.png"
mac=macos/Runner/Assets.xcassets/AppIcon.appiconset
for size in 16 32 64 128 256 512 1024; do
  magick "$tmp/macos.png" -filter Lanczos -resize "${size}x${size}" \
    "$mac/app_icon_${size}.png"
done

# ── Windows ────────────────────────────────────────────────────────────────
# One .ico carrying every size Explorer, the taskbar and Alt-Tab ask for.
render "$src" 256 "$tmp/win.png"
magick "$tmp/win.png" -define icon:auto-resize=256,128,64,48,32,24,16 \
  windows/runner/resources/app_icon.ico

# ── Linux ──────────────────────────────────────────────────────────────────
# Installed into the bundle's data/ (linux/CMakeLists.txt) and set as the
# window icon by linux/runner/my_application.cc.
mkdir -p linux/runner/resources
render "$src" 512 linux/runner/resources/app_icon.png

# ── Android ────────────────────────────────────────────────────────────────
# Legacy launcher icons; the rounded tile doubles as the shape.
for pair in mdpi:48 hdpi:72 xhdpi:96 xxhdpi:144 xxxhdpi:192; do
  render "$src" "${pair#*:}" \
    "android/app/src/main/res/mipmap-${pair%%:*}/ic_launcher.png"
done

# ── iOS ────────────────────────────────────────────────────────────────────
# iOS rejects icons with transparency and applies its own corner mask, so it
# gets a full-bleed square: the tile rect grows to the whole canvas and the
# glass rim (which the mask would crop) is dropped.
sed -e 's|<rect x="2" y="2" width="60" height="60" rx="16" fill="url(#tile)"/>|<rect width="64" height="64" fill="url(#tile)"/>|' \
    -e '/stroke="url(#rim)"/d' "$src" > "$tmp/ios.svg"
if cmp -s "$src" "$tmp/ios.svg" || grep -q 'url(#rim)"' "$tmp/ios.svg"; then
  echo "gen_icons: the SVG's tile/rim markup changed; update the iOS variant" >&2
  exit 1
fi
ios=ios/Runner/Assets.xcassets/AppIcon.appiconset
for file in "$ios"/Icon-App-*.png; do
  # Icon-App-83.5x83.5@2x.png -> 83.5 points at 2x = 167px.
  spec="${file##*/Icon-App-}"
  points="${spec%%x*}"
  scale="${spec##*@}"
  scale="${scale%x.png}"
  px="$(awk -v p="$points" -v s="$scale" 'BEGIN { printf "%d", p * s + 0.5 }')"
  render "$tmp/ios.svg" "$px" "$tmp/ios.png"
  # PNG24: forces plain RGB; otherwise ImageMagick may keep an (opaque)
  # alpha channel, which App Store validation still rejects.
  magick "$tmp/ios.png" -background '#1f1f1f' -alpha remove -alpha off \
    "PNG24:$file"
done

echo "icons regenerated from $src"

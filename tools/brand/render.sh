#!/bin/bash
# Renders the site's raster images from tools/brand/card.html with headless
# Chrome (macOS): the social card and the app icons. Re-run after changing the
# mark or the card, and commit the PNGs it writes to site/assets/.
#   tools/brand/render.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

chrome="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[ -x "$chrome" ] || { echo "render.sh: Chrome not found at $chrome (set CHROME=…)" >&2; exit 1; }
src="file://$PWD/tools/brand/card.html"
out=site/assets
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

shot() { # shot <url> <width> <height> <out.png>
  "$chrome" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=1 \
    --user-data-dir="$tmp/profile" --virtual-time-budget=4000 \
    --window-size="$2,$3" --screenshot="$4" "$1" >/dev/null 2>&1
  [ -s "$4" ] || { echo "render.sh: Chrome wrote no $4" >&2; exit 1; }
  echo "  $4 (${2}×${3})"
}

echo "rendering into $out/"
shot "$src" 1200 630 "$out/og.png"
for s in 48 180 192 512; do
  name="icon-$s.png"
  [ "$s" = 48 ] && name="favicon-48.png"
  [ "$s" = 180 ] && name="apple-touch-icon.png"
  shot "$src?icon=$s" "$s" "$s" "$out/$name"
done

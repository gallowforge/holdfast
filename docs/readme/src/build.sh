#!/usr/bin/env bash
# Rebuild every README picture of Holdfast in Glass, dark and light, wide and phone.
#
#   make shots                                  builds bin/holdfast, installs this folder's two packages, runs this
#   docs/readme/src/build.sh [the binary]       the same without the build (the binary defaults to bin/holdfast)
#
# Needs: node with this folder's packages (npm ci: playwright and three), a Chromium that playwright can launch
# (a GPU helps; the scenes are WebGL), ImageMagick, and python3 with Pillow and NumPy.
# capture.cjs starts `holdfast serve --demo` on a free port and photographs its page in the dark theme and in the
# light theme, runs the command-line sessions for real in a throwaway folder, and photographs the ledger twelve
# times a second while the demo's machine is put back and then changed twice. render.cjs composes the glass scenes
# from layouts.js; ImageMagick writes the stills as WebP. The hero is a loop at 12 frames a second: it opens on
# the headline that three facts are no longer true, puts the machine back until every check holds, changes it
# again and comes back. render.cjs writes one turn of it as frames and loop.py packs them.
set -euo pipefail
SRC=$(cd "$(dirname "$0")" && pwd)
OUT=$(cd "$SRC/.." && pwd)
ROOT=$(cd "$OUT/../.." && pwd)
BUILD="$OUT/build"
BIN="${1:-$ROOT/bin/holdfast}"
PANELS="numbers recall refused proof policy shared"

node "$SRC/capture.cjs" "$BIN"
mkdir -p "$BUILD/png" "$BUILD/loop"
# frames left by an earlier build with a longer loop would be packed into this one
find "$BUILD/png" "$BUILD/loop" -type f -name '*.png' -delete

jobs=()
for t in dark light; do
  for l in $PANELS; do
    jobs+=("$l:$t:still:$BUILD/png/$l-$t.png" "$l-phone:$t:still:$BUILD/png/$l-$t-phone.png")
  done
  jobs+=("hero:$t:anim:loop:$BUILD/loop/hero-$t" "hero-phone:$t:anim:loop:$BUILD/loop/hero-$t-phone")
done
node "$SRC/render.cjs" "${jobs[@]}"
for f in "$BUILD"/png/*.png; do
  magick "$f" -strip -quality 88 -define webp:method=6 "$OUT/$(basename "$f" .png).webp"
done
# the last number is the WebP quality: the phone hero has more picture to move and half the size to do it in
for t in dark light; do
  python3 "$SRC/loop.py" "$BUILD/loop/hero-$t" "$OUT/hero-$t.webp" 12 60
  python3 "$SRC/loop.py" "$BUILD/loop/hero-$t-phone" "$OUT/hero-$t-phone.webp" 12 50
done

# The plate for the repository's social preview (Settings, Social preview on GitHub) and for the demo's link
# card, 1280 x 640, dark.
node "$SRC/render.cjs" "social:dark:still:$OUT/social.png"
magick "$OUT/social.png" -strip -define png:compression-level=9 "$OUT/social.png"

du -ch "$OUT"/*.webp "$OUT/social.png" | tail -1

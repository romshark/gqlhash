#!/bin/sh
# Rasterizes the icon SVGs in public/ into the PNG and ICO sizes the platforms
# that can't read SVG need. The SVGs are the source of truth; the outputs are
# committed so neither CI nor a plain `pnpm build` needs a rasterizer.
#
# Requires librsvg (rsvg-convert) and ImageMagick (magick):
#   brew install librsvg imagemagick
set -eu

cd "$(dirname "$0")/../public"

for tool in rsvg-convert magick; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "$tool not found: brew install librsvg imagemagick" >&2
		exit 1
	fi
done

# Rounded icon for contexts that don't mask.
rsvg-convert -w 192 -h 192 icon.svg -o icon-192.png
rsvg-convert -w 512 -h 512 icon.svg -o icon-512.png

# Full-bleed icon for platforms that apply their own mask.
rsvg-convert -w 512 -h 512 icon-maskable.svg -o icon-maskable-512.png
rsvg-convert -w 180 -h 180 icon-maskable.svg -o apple-touch-icon.png

# Legacy favicon for browsers without SVG icon support. rsvg-convert renders the
# light-scheme branch of the media query in favicon.svg, and magenta reads on a
# light and a dark tab strip alike.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for size in 16 32 48; do
	rsvg-convert -w "$size" -h "$size" favicon.svg -o "$tmp/$size.png"
done
magick "$tmp/16.png" "$tmp/32.png" "$tmp/48.png" favicon.ico

ls -l icon-192.png icon-512.png icon-maskable-512.png apple-touch-icon.png \
	favicon.ico

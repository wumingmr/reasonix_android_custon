#!/usr/bin/env bash
# Produce and verify a fresh image before replacing a previously built artifact.
set -euo pipefail

app="${1:?usage: package-desktop-dmg.sh <app> <output.dmg> <app-name>}"
output="${2:?missing output image}"
appname="${3:?missing app name}"
mkdir -p "$(dirname "$output")"
staging=$(mktemp -d "$(dirname "$output")/.desktop-dmg.XXXXXX")
trap 'rm -rf "$staging"' EXIT
mkdir "$staging/input"
cp -R "$app" "$staging/input/${appname}.app"
image="$staging/candidate.dmg"

# Some create-dmg versions report Finder customization errors after producing
# a valid image. Verify that newly produced image, never the previous output.
create-dmg \
	--volname "$appname" \
	--window-size 540 380 \
	--icon-size 110 \
	--icon "${appname}.app" 150 190 \
	--app-drop-link 390 190 \
	--no-internet-enable \
	"$image" "$staging/input" || true
[ -s "$image" ] || { echo "create-dmg did not produce a fresh image" >&2; exit 1; }
hdiutil verify "$image"
mv -f "$image" "$output"

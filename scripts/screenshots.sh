#!/usr/bin/env bash
# Regenerates docs/design/screens from invented data: the Vite dev server,
# test/e2e/design/capture.mjs on its fixtures, then every metadata block
# stripped from the images (exiftool). Needs `make web`, the e2e packages
# (cd test/e2e && npm ci), a Chromium (BROWSER, default the one Playwright
# installed) and exiftool.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
port=5199
out=docs/design/screens
browser=${BROWSER:-$(ls "$HOME"/.cache/ms-playwright/chromium-*/chrome-linux*/chrome 2>/dev/null | tail -n 1)}
[[ -x $browser ]] || { echo "screenshots: no Chromium; set BROWSER" >&2; exit 1; }
command -v exiftool >/dev/null || { echo "screenshots: exiftool is not installed" >&2; exit 1; }

(cd web && exec npx vite --port "$port" --strictPort >/dev/null 2>&1) &
vite=$!
trap 'kill "$vite" 2>/dev/null || true' EXIT
for _ in $(seq 1 60); do
	curl -s -o /dev/null "http://127.0.0.1:$port/" && break
	sleep 0.5
done

tmp=$(mktemp -d)
(cd test/e2e && BROWSER=$browser DESIGN_URL=http://127.0.0.1:$port node design/capture.mjs "$tmp")
exiftool -q -all= -overwrite_original "$tmp"
rm -f "$out"/*.jpg
cp "$tmp"/*.jpg "$out"/
rm -rf "$tmp"
echo "screenshots: $(find "$out" -name '*.jpg' | wc -l) images in $out"

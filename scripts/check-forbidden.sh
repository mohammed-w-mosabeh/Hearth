#!/usr/bin/env bash
# Fails if forbidden patterns appear in source (SECURITY S6, S7).
# golangci-lint (depguard/forbidigo) covers os/exec; this catches what it cannot.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

fail=0

report() {
	local what=$1 hits=$2
	if [[ -n $hits ]]; then
		echo "forbidden: $what"
		echo "$hits"
		fail=1
	fi
}

# No shell invocation anywhere in Go code.
report "shell invocation (sh -c / bash -c)" \
	"$(git grep -nE '\b(ba)?sh\b["`]?[[:space:],]+["`]?-c\b' -- '*.go' || true)"

# No raw HTML injection in the web UI.
report "{@html} in Svelte" \
	"$(git grep -nF '{@html' -- 'web/src' || true)"
report "innerHTML / outerHTML / insertAdjacentHTML" \
	"$(git grep -nE '\b(inner|outer)HTML\b|insertAdjacentHTML' -- 'web/src' || true)"

# No page may ship as a promise.
report "placeholder page text" \
	"$(git grep -niE 'arrives in phase|coming soon|not implemented yet|available in a later phase' -- 'web/src' ':!*.test.ts' || true)"

# The fixture screens (web/design.html) stay out of the production bundle.
report "production code importing web/src/design" \
	"$(git grep -nE "from '[^']*/design/|import '[^']*/design/" -- 'web/src' ':!web/src/design' || true)"

# Fonts have their own budget (Gate 8).
fonts=$(cat web/src/assets/fonts/*.woff2 | wc -c)
report "fonts over 250 KB" "$([ "$fonts" -le 256000 ] || echo "$fonts bytes")"

# Logical CSS only, so the layout mirrors for right-to-left languages.
report "physical-direction CSS (use margin-inline-start, text-align: start, ...)" \
	"$(git grep -nE '(margin|padding|border)-(left|right)\b|text-align: *(left|right)\b|float: *(left|right)\b' -- 'web/src/*.svelte' 'web/src/*.css' | grep -v 'rtl-physical' || true)"

exit $fail

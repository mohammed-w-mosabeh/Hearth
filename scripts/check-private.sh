#!/usr/bin/env bash
# Fails if a private pattern appears in the tracked files, or with --history
# in any commit, message, or author, committer and tagger line of every ref.
#
# The patterns are private themselves, so they live in a local file that is
# never published: .private-patterns at the top of the repository
# (gitignored), or the file named by PRIVATE_PATTERNS. One pattern per line:
#   text          a fixed string, case-sensitive
#   re:<regex>    an extended regular expression
#   # comment
# A hit is reported as file, line and pattern number, never the value.
# Without the file the check is skipped, and says so.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
patterns=${PRIVATE_PATTERNS:-.private-patterns}
mode=tree
[[ ${1:-} == --history ]] && mode=history

if [[ ! -f $patterns ]]; then
	echo "check-private: skipped, no pattern file ($patterns)"
	exit 0
fi

# Lock files hold only registry names and hashes, where a short pattern can
# match by accident.
exclude=(':!go.sum' ':!web/package-lock.json' ':!test/e2e/package-lock.json')

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
if [[ $mode == history ]]; then
	git log --all -p --no-color --format='commit %H%nauthor %an <%ae>%ncommitter %cn <%ce>%n%B' -- . "${exclude[@]}" >"$tmp"
	# Without a pathspec, so no commit is left out: the one above prunes a
	# commit that touches only an excluded file, and its message with it.
	git log --all --no-color --format='commit %H%nauthor %an <%ae>%ncommitter %cn <%ce>%n%B' >>"$tmp"
	git for-each-ref refs/tags --format='tag %(refname:short)%0atagger %(taggername) %(taggeremail)%0a%(contents)' >>"$tmp"
fi

fail=0 n=0 count=0
while IFS= read -r line || [[ -n $line ]]; do
	n=$((n + 1))
	[[ -z $line || $line == \#* ]] && continue
	count=$((count + 1))
	flag=-F pat=$line
	if [[ $line == re:* ]]; then
		flag=-E pat=${line#re:}
	fi
	if [[ $mode == tree ]]; then
		hits=$(git grep -I -n "$flag" -e "$pat" -- . "${exclude[@]}" | cut -d: -f1,2 || true)
	else
		hits=$(grep -c "$flag" -e "$pat" "$tmp" || true)
		[[ $hits == 0 ]] && hits='' || hits="$hits lines in the history"
	fi
	if [[ -n $hits ]]; then
		echo "private pattern #$n (line $n of the pattern file):"
		echo "$hits" | sed 's/^/  /'
		fail=1
	fi
done <"$patterns"

if [[ $fail -ne 0 ]]; then
	echo "check-private: private data found ($mode)"
	exit 1
fi
echo "check-private: clean ($mode, $count patterns)"

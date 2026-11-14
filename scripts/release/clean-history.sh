#!/usr/bin/env bash
# Makes the public history of Hearth from the private repository, which is
# only read: a fresh clone into a new directory, rewritten there with
# git filter-repo, then checked. It stops at the first finding, and only a
# clone that passed every check is marked as ready for publish.sh.
#
#   scripts/release/clean-history.sh <private-repo> <new-directory> <noreply-email>
#
# <private-repo>   the working copy of the private repository. Its two local,
#                  gitignored files are read: .private-patterns (what must
#                  never appear) and .private-replacements (git filter-repo
#                  --replace-text rules: literal==>replacement).
# <new-directory>  must not exist.
# <noreply-email>  <id>+<login>@users.noreply.github.com; every other
#                  personal address in commit and tag metadata becomes it.
#                  Names are kept.
#
# SOURCE_BRANCH (default main) is the branch that becomes the public main.
# Only it and the tags on it are cloned: no other branch enters.
set -euo pipefail

if [[ $# -ne 3 ]]; then
	sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
fi
src=$(realpath "$1")
dest=$2
noreply=$3
branch=${SOURCE_BRANCH:-main}

# Private working files that are not part of the public project.
drop=(.claude CLAUDE.md KICKOFF.md docs/plan)

die() {
	echo "clean-history: $*" >&2
	exit 1
}

command -v git-filter-repo >/dev/null || git filter-repo --version >/dev/null 2>&1 || die "git filter-repo is not installed (sudo apt install git-filter-repo)"
command -v gitleaks >/dev/null || die "gitleaks is not installed"
[[ -d $src/.git ]] || die "$src is not a git working copy"
[[ -f $src/.private-patterns ]] || die "$src/.private-patterns is missing"
[[ -f $src/.private-replacements ]] || die "$src/.private-replacements is missing"
[[ ! -e $dest ]] || die "$dest exists already"
[[ $noreply =~ ^[0-9]+\+[A-Za-z0-9-]+@users\.noreply\.github\.com$ ]] || die "the third argument is not a GitHub noreply address"
git -C "$src" rev-parse --verify --quiet "refs/heads/$branch" >/dev/null || die "no branch $branch in $src"

echo "== clone $branch and its tags (the private repository is only read)"
git clone --quiet --no-local --single-branch --branch "$branch" "$src" "$dest"
dest=$(realpath "$dest")
cd "$dest"
touch .git/hearth-not-clean

# Every address in author, committer and tagger lines that is not a noreply
# address already. Built here, so no old address is written in this script.
mailmap=$(mktemp)
work=$(mktemp)
strip=
trap 'rm -f "$mailmap" "$work" "$strip"' EXIT
{
	git log --all --format='%ae%n%ce'
	git for-each-ref refs/tags --format='%(taggeremail)' | tr -d '<>'
} | sort -u | grep -v -e '^$' -e '@users\.noreply\.github\.com$' -e '^noreply@' >"$work" || true
while IFS= read -r old; do
	printf '<%s> <%s>\n' "$noreply" "$old"
done <"$work" >"$mailmap"
echo "== $(wc -l <"$mailmap") address(es) to rewrite"

# Binary files (screenshots, fonts) cannot be searched for private text, and
# the replacement rules do not reach them: only the versions in the branch's
# own tree, which were made from invented data, enter the public history.
# old_binaries prints the blob IDs of every other version.
old_binaries() {
	local tip=$1 paths ids
	paths=$(mktemp) ids=$(mktemp)
	git log --all -m --no-renames --numstat --format= | awk -F'\t' '$1 == "-" { print $3 }' | sort -u >"$paths"
	git ls-tree -r "$tip" | awk '{ print $3 }' | sort -u >"$ids"
	git rev-list --all --objects | awk -v paths="$paths" -v ids="$ids" '
		BEGIN {
			while ((getline l < paths) > 0) binary[l] = 1
			while ((getline l < ids) > 0) kept[l] = 1
		}
		{ id = $1; sub(/^[0-9a-f]+ ?/, ""); if (($0 in binary) && !(id in kept)) print id }'
	rm -f "$paths" "$ids"
}
strip=$(mktemp)
old_binaries "$branch" >"$strip"
echo "== $(wc -l <"$strip") old version(s) of binary files to leave out"

echo "== rewrite"
paths=()
for p in "${drop[@]}"; do paths+=(--path "$p"); done
git filter-repo --quiet \
	--invert-paths "${paths[@]}" \
	--replace-text "$src/.private-replacements" \
	--replace-message "$src/.private-replacements" \
	--strip-blobs-with-ids "$strip" \
	--mailmap "$mailmap"
# Renamed only now: a rename before the rewrite adds a reflog entry, and
# git filter-repo then refuses the clone as not fresh.
[[ $branch == main ]] || git branch --quiet -m "$branch" main

echo "== check: dropped paths"
left=$(git log --all --format= --name-only -- "${drop[@]}" | sort -u)
[[ -z $left ]] || die "dropped paths are still in the history"

echo "== check: binary files"
old_binaries main >"$work"
[[ ! -s $work ]] || die "$(wc -l <"$work") old version(s) of binary files are still in the history"

echo "== check: commit and tag metadata"
{
	git log --all --format='%ae%n%ce'
	git for-each-ref refs/tags --format='%(taggeremail)' | tr -d '<>'
} | sort -u | grep -v -e '^$' -e '@users\.noreply\.github\.com$' -e '^noreply@' >"$work" || true
[[ ! -s $work ]] || die "$(wc -l <"$work") address(es) in the metadata are not noreply addresses"

echo "== check: only main and tags"
others=$(git for-each-ref --format='%(refname)' | grep -v -e '^refs/heads/main$' -e '^refs/tags/' || true)
[[ -z $others ]] || die "unexpected refs: $others"
[[ -z $(git remote) ]] || die "the clone still has a remote"

echo "== check: private patterns, whole history and tree"
[[ -x scripts/check-private.sh ]] || die "scripts/check-private.sh is not in $branch"
PRIVATE_PATTERNS=$src/.private-patterns ./scripts/check-private.sh --history
PRIVATE_PATTERNS=$src/.private-patterns ./scripts/check-private.sh

echo "== check: gitleaks, whole history and tree"
config=()
[[ -f .gitleaks.toml ]] && config=(-c .gitleaks.toml)
gitleaks git --no-banner --redact=100 --log-opts=--all "${config[@]}" .
gitleaks dir --no-banner --redact=100 "${config[@]}" .

echo "== check: the tree is the private tree without the dropped paths"
# Proves the replacement rules and the cleaned working tree agree: after the
# rewrite the two must be the same, file for file.
want=$(git -C "$src" ls-tree -r "$branch" | grep -v -E "$(printf '\t(%s)(/|$)|' "${drop[@]}" | sed 's/|$//; s/\./\\./g')" | sha256sum)
have=$(git ls-tree -r main | sha256sum)
[[ $want == "$have" ]] || die "the rewritten tree differs from $branch without the dropped paths; a replacement rule and the cleaned tree disagree (compare with: git diff --no-index)"

rm -f .git/hearth-not-clean
# Every ref, not main alone: publish.sh refuses a clone in which main or a
# tag moved, or a tag was added, after these checks.
git for-each-ref --format='%(objectname) %(refname)' >.git/hearth-clean-ok
echo
echo "clean-history: passed. $(git rev-list --count main) commits, $(git tag | wc -l) tags in $dest"
echo "Next: scripts/release/publish.sh $dest"

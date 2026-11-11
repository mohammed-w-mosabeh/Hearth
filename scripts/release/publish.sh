#!/usr/bin/env bash
# Pushes a cleaned clone made by clean-history.sh to the public repository:
# main and all tags, nothing else. Run it only after the private repository
# was renamed on GitHub and a new, empty, public repository was created
# under the old name.
#
#   PRIVATE_PATTERNS=<private-repo>/.private-patterns scripts/release/publish.sh <cleaned-clone>
#
# It refuses to run when the clone did not pass clean-history.sh, changed
# since, has a remote already, or when the target is not exactly the new
# empty repository: after a rename GitHub redirects the old name to the
# private repository until the new one exists, so the name alone proves
# nothing.
set -euo pipefail

owner=mohammed-w-mosabeh
name=Hearth
url=git@github.com:$owner/$name.git

die() {
	echo "publish: $*" >&2
	exit 1
}

[[ $# -eq 1 ]] || { sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
cd "$1"
top=$(git rev-parse --show-toplevel)
cd "$top"

[[ ! -e .git/hearth-not-clean ]] || die "this clone did not pass clean-history.sh"
[[ -f .git/hearth-clean-ok ]] || die "this clone was not made by clean-history.sh"
[[ $(cat .git/hearth-clean-ok) == "$(git rev-parse main)" ]] || die "main changed since clean-history.sh passed"
[[ -z $(git status --porcelain) ]] || die "the working tree is not clean"
[[ -z $(git remote) ]] || die "the clone has a remote already: $(git remote -v | head -n 1)"
others=$(git for-each-ref --format='%(refname)' | grep -v -e '^refs/heads/main$' -e '^refs/tags/' || true)
[[ -z $others ]] || die "unexpected refs: $others"

echo "== private patterns once more"
[[ -n ${PRIVATE_PATTERNS:-} && -f $PRIVATE_PATTERNS ]] || die "set PRIVATE_PATTERNS to the private repository's .private-patterns"
./scripts/check-private.sh --history
./scripts/check-private.sh

echo "== the target repository"
command -v gh >/dev/null || die "gh is not installed"
info=$(gh repo view "$owner/$name" --json nameWithOwner,visibility,isEmpty,isFork) || die "cannot read $owner/$name on GitHub"
[[ $(jq -r .nameWithOwner <<<"$info") == "$owner/$name" ]] || die "$owner/$name resolves to $(jq -r .nameWithOwner <<<"$info"): the new repository does not exist yet"
[[ $(jq -r .visibility <<<"$info") == PUBLIC ]] || die "$owner/$name is not public: is it the private repository?"
[[ $(jq -r .isEmpty <<<"$info") == true ]] || die "$owner/$name is not empty"
[[ -z $(git ls-remote "$url" 2>/dev/null) ]] || die "$url has refs already"

echo "== push main and tags to $url"
git remote add origin "$url"
git push origin main
git push origin --tags
echo "publish: done. Now enable private vulnerability reporting: Settings -> Code security."

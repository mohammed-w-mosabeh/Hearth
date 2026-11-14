#!/usr/bin/env bash
# Builds the release .deb from a clean checkout and writes dist/SHA256SUMS.
#
# The build is meant to be reproducible: -trimpath and an empty build ID for
# the Go binaries, and SOURCE_DATE_EPOCH (the commit's time) for the file
# times inside the package. The version comes from the git tag
# (scripts/version.sh). The package is not signed yet; SHA256SUMS is what a
# user checks a download against.
#
#   scripts/release/build.sh          build what is checked out
#   scripts/release/build.sh --twice  build twice and fail if the two differ
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
if [[ -n $(git status --porcelain) ]]; then
	echo "release build: the working tree is not clean" >&2
	exit 1
fi

SOURCE_DATE_EPOCH=$(git log -1 --format=%ct)
export SOURCE_DATE_EPOCH
export TZ=UTC LC_ALL=C
version=$(./scripts/version.sh)
deb=dist/hearth_${version}_amd64.deb

build() {
	rm -rf bin web/dist "$deb"
	make deb
}

build
if [[ ${1:-} == --twice ]]; then
	first=$(sha256sum "$deb" | cut -d' ' -f1)
	build
	second=$(sha256sum "$deb" | cut -d' ' -f1)
	if [[ $first != "$second" ]]; then
		echo "release build: two builds differ ($first, $second)" >&2
		exit 1
	fi
	echo "release build: reproducible ($first)"
fi

(cd dist && sha256sum "$(basename "$deb")" >SHA256SUMS)
echo "release build: $deb"
cat dist/SHA256SUMS

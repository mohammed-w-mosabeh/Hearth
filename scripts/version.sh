#!/usr/bin/env bash
# Prints Hearth's version from git describe: 1.0.0 on the tag,
# 1.0.0+git1.505fb84 after it, for the .deb and both binaries.
set -euo pipefail

if ! d=$(git describe --tags --match 'v[0-9]*' --abbrev=7 2>/dev/null); then
	echo "0.0.0+git0.$(git rev-parse --short=7 HEAD)" # no tag here (a shallow clone)
	exit 0
fi
d=${d#v}
if [[ $d =~ ^(.+)-([0-9]+)-g([0-9a-f]+)$ ]]; then
	echo "${BASH_REMATCH[1]}+git${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
else
	echo "$d"
fi

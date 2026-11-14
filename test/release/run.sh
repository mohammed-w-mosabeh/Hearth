#!/usr/bin/env bash
# Release checks that `make itest` does not make (see check.sh), in the same
# throwaway Debian 13 + systemd container; nothing of the host is mounted.
#   test/release/run.sh <oldest-release.deb> <new.deb> <fakedocker-binary>
# The old package is built from the oldest tag, for example:
#   git worktree add /tmp/hearth-v1.0.0 v1.0.0 && cd /tmp/hearth-v1.0.0 &&
#   make deb VERSION=1.0.0 NFPM=<this checkout>/.tools/bin/nfpm
set -euo pipefail

usage="usage: run.sh oldest.deb new.deb path/to/fakedocker"
old=${1:?$usage}
new=${2:?$usage}
fakedocker=${3:?$usage}
here=$(cd "$(dirname "$0")" && pwd)
image=hearth-itest:debian13
name=hearth-release-$$

docker build --quiet --tag "$image" --file "$here/../itest/Dockerfile" "$here/.." >/dev/null
docker run --detach --rm --name "$name" \
	--cgroupns=private \
	--tmpfs /run --tmpfs /run/lock \
	--cap-add SYS_ADMIN \
	--cap-add SYS_PTRACE \
	--cap-add NET_ADMIN \
	--cap-add DAC_READ_SEARCH \
	--security-opt seccomp=unconfined \
	--security-opt apparmor=unconfined \
	"$image" >/dev/null
trap 'docker stop --time 5 "$name" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 100); do
	docker exec "$name" test -S /run/systemd/private 2>/dev/null && break
	sleep 0.2
done
docker exec "$name" timeout 90 systemctl is-system-running --wait >/dev/null || true

docker cp "$old" "$name:/root/old.deb"
docker cp "$new" "$name:/root/new.deb"
docker cp "$fakedocker" "$name:/root/fakedocker"
docker cp "$here/check.sh" "$name:/root/release-check.sh"
docker exec "$name" bash /root/release-check.sh /root/old.deb /root/new.deb

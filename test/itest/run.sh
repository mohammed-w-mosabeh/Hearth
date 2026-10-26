#!/usr/bin/env bash
# Host side of `make itest`: boot a throwaway Debian 13 + systemd container,
# install the .deb, run the checks, purge. The container is --rm.
set -euo pipefail

deb=${1:?usage: run.sh path/to/hearth.deb path/to/fakedocker}
fakedocker=${2:?usage: run.sh path/to/hearth.deb path/to/fakedocker}
here=$(cd "$(dirname "$0")" && pwd)
image=hearth-itest:debian13
name=hearth-itest-$$

docker build --quiet --tag "$image" "$here" >/dev/null

# systemd needs its own cgroup namespace, tmpfs /run, and to mount
# namespaces for the unit sandbox. No host paths are mounted.
docker run --detach --rm --name "$name" \
	--cgroupns=private \
	--tmpfs /run --tmpfs /run/lock \
	--cap-add SYS_ADMIN \
	--security-opt seccomp=unconfined \
	--security-opt apparmor=unconfined \
	"$image" >/dev/null
trap 'docker stop --time 5 "$name" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 100); do
	docker exec "$name" test -S /run/systemd/private 2>/dev/null && break
	sleep 0.2
done
state=$(docker exec "$name" timeout 90 systemctl is-system-running --wait || true)
echo "container systemd: $state"
case $state in running | degraded) ;; *)
	docker exec "$name" systemctl --failed --no-pager || true
	exit 1
	;;
esac

docker cp "$deb" "$name:/root/hearth.deb"
docker cp "$fakedocker" "$name:/root/fakedocker"
docker exec "$name" hearth-itest-check /root/hearth.deb

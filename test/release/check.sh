#!/usr/bin/env bash
# Container side of test/release/run.sh (root, in the itest image): what
# `make itest` does not cover before a release. An upgrade from the oldest
# released package keeps the user's data; hearthd survives its own restart,
# Docker going away, polkit going away and a full disk.
set -euo pipefail

old=$1 new=$2
user=tester uid=1000
failures=0
pass() { echo "PASS  $*"; }
fail() {
	echo "FAIL  $*"
	failures=$((failures + 1))
}
check() {
	local what=$1
	shift
	if "$@" >/dev/null 2>&1; then pass "$what"; else fail "$what"; fi
}
not() { ! "$@"; }
asuser() {
	runuser -u "$user" -- env XDG_RUNTIME_DIR=/run/user/$uid \
		DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus "$@"
}
wait_until() {
	local n=$(($1 * 10))
	shift
	for _ in $(seq "$n"); do "$@" >/dev/null 2>&1 && return 0; sleep 0.1; done
	return 1
}
cfg=/home/$user/.config/hearth
data=/home/$user/.local/share/hearth
port() { sed -nE 's/.*"port": *([0-9]+).*/\1/p' $cfg/config.json; }
health() { curl -fsS "http://127.0.0.1:$(port)/api/v1/health"; }
api_key() { curl -s -o /dev/null -w '%{redirect_url}' "http://127.0.0.1:$(port)/auth?t=$(cat $cfg/token)" | sed 's/.*#k=//'; }
get() { curl -s -H "Authorization: Bearer $key" "http://127.0.0.1:$(port)/api/v1/$1"; }
code() { curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$(port)/api/v1/$1"; }
put() { curl -s -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$(port)" -H 'Content-Type: application/json' -d "$2" "http://127.0.0.1:$(port)/api/v1/$1"; }
active() { asuser systemctl --user is-active hearthd.service; }
start() {
	asuser systemctl --user daemon-reload
	asuser systemctl --user restart hearthd.service
	wait_until 20 health
}

echo "== upgrade from the oldest release"
wait_until 30 systemctl is-active user@$uid.service || fail "user manager not running"
apt-get install -y -qq "$old" >/dev/null
oldver=$(dpkg-query -W -f='${Version}' hearth)
check "old package installed ($oldver)" test -n "$oldver"
check "old hearthd answers" start
key=$(api_key)
fav='{"ids":["org.example.Notes"]}'
check "a setting is saved by the old version" test "$(put settings/apps.favorites "$fav")" = 204 -o "$(put settings/apps.favorites "$fav")" = 200
port_before=$(port)
apt-get install -y -qq "$new" >/dev/null
newver=$(dpkg-query -W -f='${Version}' hearth)
check "new package installed ($newver)" test "$newver" != "$oldver"
check "new hearthd answers on the old database" start
check "it is the new binary" test "$(asuser /usr/bin/hearthd --version)" = "$newver"
key=$(api_key)
check "the setting is still there" test "$(get settings/apps.favorites | tr -d ' \n')" = "$fav"
check "the port is the same" test "$(port)" = "$port_before"
check "the database is still 0600" test "$(stat -c %a $data/hearth.db)" = 600
check "no migration error in the journal" not grep -qiE 'migrat.*(fail|error)' <<<"$(journalctl _UID=$uid --no-pager -n 200)"

echo "== hearthd restarts with a session open"
check "the session key works" test "$(code settings/apps.favorites)" = 200
asuser systemctl --user restart hearthd.service
check "healthy again within 20 s" wait_until 20 health
check "the old key is refused or still valid, never a 5xx" grep -qE '^(200|401|403)$' <<<"$(code settings/apps.favorites)"
key=$(api_key)
check "a new key works after the restart" test "$(code settings/apps.favorites)" = 200
check "killed, systemd starts it again" bash -c "pkill -KILL -u $user -x hearthd; sleep 1; for i in \$(seq 200); do curl -fsS http://127.0.0.1:$(port)/api/v1/health >/dev/null 2>&1 && exit 0; sleep 0.1; done; exit 1"
key=$(api_key)

echo "== Docker stops in the middle of a session"
usermod -aG hearth "$user"
systemctl restart user@$uid.service
wait_until 30 systemctl is-active user@$uid.service
wait_until 20 asuser systemctl --user is-active hearthd.service || asuser systemctl --user start hearthd.service
wait_until 20 health || fail "hearthd after the group change"
key=$(api_key)
systemd-run --quiet --unit=fakedocker /root/fakedocker
wait_until 10 test -S /run/docker.sock || fail "fake docker started"
check "Docker page data with the engine up" test "$(code docker)" = 200
systemctl stop fakedocker
rm -f /run/docker.sock
c=$(code docker)
check "with the engine gone the API answers without a server error ($c)" test "$c" -lt 500
check "hearthd is still running" active
check "and healthy" health

echo "== polkit is not running"
systemctl stop polkit.service
systemctl mask --runtime --quiet polkit.service
c=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$(port)" -H 'Content-Type: application/json' -d '{"target":"system/cron.service","preview":true}' "http://127.0.0.1:$(port)/api/v1/actions/unit/restart")
check "a privileged action answers without a crash ($c)" test "$c" -gt 0
check "hearthd is still running" active
for p in services pkgs net/state security docker alerts/events; do
	c=$(code "$p")
	check "GET /$p answers without a server error ($c)" test "$c" -gt 0 -a "$c" -lt 500
done
systemctl unmask --runtime --quiet polkit.service
systemctl start polkit.service || true

echo "== the disk fills while hearthd runs"
asuser systemctl --user stop hearthd.service
cp -a $data /root/data.keep
mount -t tmpfs -o size=8m,uid=$uid,gid=$uid,mode=0700 tmpfs $data
cp -a /root/data.keep/. $data/
check "hearthd starts on the small disk" start
key=$(api_key)
runuser -u "$user" -- dd if=/dev/zero of=$data/fill bs=4k 2>/dev/null || true
check "the data folder is full" not runuser -u "$user" -- sh -c "echo x > $data/probe"
sleep 12 # a few sampler writes fail meanwhile
check "hearthd is still running" active
check "and healthy" health
c=$(code settings/apps.favorites)
check "reads still answer ($c)" test "$c" = 200
c=$(put settings/apps.favorites '{"ids":["org.example.Photos"]}')
echo "  a write on the full disk answers $c"
check "the write is refused with an error, not accepted" test "$c" -ge 500
check "hearthd is still running after it" active
rm -f $data/fill
c=$(put settings/apps.favorites "$fav")
check "with space back the write succeeds ($c)" test "$c" = 200 -o "$c" = 204

echo "== hearthd starts on a full disk"
asuser systemctl --user stop hearthd.service
runuser -u "$user" -- dd if=/dev/zero of=$data/fill bs=4k 2>/dev/null || true
asuser systemctl --user start hearthd.service || true
if wait_until 8 health; then
	pass "hearthd serves on a full disk"
else
	echo "  hearthd does not start on a full disk; its last words:"
	journalctl _UID=$uid --no-pager -n 40 -o cat | grep -i '"level":"ERROR"' | tail -n 1 | cut -c1-220 | sed 's/^/    /'
	check "it says why in the journal (a disk or database error)" grep -qiE 'no space|disk|full|database|sqlite' <<<"$(journalctl _UID=$uid --no-pager -n 40 -o cat)"
fi
asuser systemctl --user stop hearthd.service || true
asuser systemctl --user reset-failed hearthd.service || true
umount $data
check "with space back, hearthd starts on the kept data" start
key=$(api_key)
check "and the setting is still there" test "$(get settings/apps.favorites | tr -d ' \n')" = "$fav"

echo
if [ "$failures" -eq 0 ]; then
	echo "release checks: all passed"
else
	echo "release checks: $failures FAILED"
	exit 1
fi

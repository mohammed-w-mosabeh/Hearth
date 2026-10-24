#!/usr/bin/env bash
# Container side of `make itest` (runs as root inside the test container).
set -euo pipefail

deb=$1
user=tester
uid=1000
failures=0

pass() { echo "PASS  $*"; }
fail() {
	echo "FAIL  $*"
	failures=$((failures + 1))
}
check() { # check "description" command...
	local what=$1
	shift
	if "$@" >/dev/null 2>&1; then pass "$what"; else fail "$what"; fi
}
asuser() {
	runuser -u "$user" -- env XDG_RUNTIME_DIR=/run/user/$uid \
		DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus "$@"
}
not() { ! "$@"; }
loopback_only() { [ -n "$1" ] && ! grep -vq "127.0.0.1:" <<<"$1"; }
wildcard_listener() { ss -tlnH | awk '{print $4}' | grep -Eq "^(0\.0\.0\.0|\[::\]|\*):$1$"; }
port_open() { ss -tlnH | awk '{print $4}' | grep -Eq ":$1$"; }
# Options of the mount that actually covers path: the longest mount point
# (mountinfo field 5) that is a prefix of it; field 6 is the options.
effective_opts() {
	awk -v p="$2" '{ m = $5; if (p == m || m == "/" || index(p, m "/") == 1) if (length(m) >= best) { best = length(m); o = $6 } } END { print o }' <<<"$1"
}
home_ro() { effective_opts "$1" /home/$user/Documents | grep -q '^ro'; }
usr_ro() { effective_opts "$1" /usr/bin | grep -q '^ro'; }
data_rw() { effective_opts "$1" /home/$user/.local/share/hearth/hearth.db | grep -q '^rw'; }
log_has_refusal() { grep -Eq "binary rejected|not owned by root|not in allowlist" <<<"$1"; }
wait_until() { # wait_until seconds command...
	local n=$(($1 * 10))
	shift
	for _ in $(seq "$n"); do "$@" >/dev/null 2>&1 && return 0; sleep 0.1; done
	return 1
}

echo "== install"
apt-get install -y -qq "$deb" >/dev/null
check "package installed" dpkg -s hearth
check "binary root-owned 0755" test "$(stat -c '%U %a' /usr/bin/hearthd)" = "root 755"
check "user unit installed" test -f /usr/lib/systemd/user/hearthd.service
check "enabled globally" test -L /etc/systemd/user/default.target.wants/hearthd.service
check "desktop file installed" test -f /usr/share/applications/hearth.desktop

echo "== start as $user"
wait_until 30 systemctl is-active user@$uid.service || fail "user manager not running"
asuser systemctl --user daemon-reload
asuser systemctl --user start hearthd.service || true
if wait_until 15 asuser systemctl --user is-active hearthd.service; then
	pass "hearthd.service active"
else
	fail "hearthd.service active"
	asuser journalctl --user -u hearthd --no-pager -n 50 || journalctl _UID=$uid --no-pager -n 50
fi

cfg=/home/$user/.config/hearth/config.json
wait_until 10 test -f $cfg || true
port=$(sed -nE 's/.*"port": *([0-9]+).*/\1/p' $cfg 2>/dev/null || true)
echo "port: ${port:-?}"

check "health 200" wait_until 10 curl -fsS "http://127.0.0.1:$port/api/v1/health"
check "rebinding Host rejected" test "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.test' "http://127.0.0.1:$port/api/v1/health")" = 403

# By port: ss -p cannot map sockets to processes without CAP_SYS_PTRACE,
# which Docker drops.
listeners=$(ss -tlnH "sport = :$port" | awk '{print $4}')
echo "listeners on :$port:"
echo "${listeners:-  (none)}" | sed 's/^/  /'
check "listens on 127.0.0.1 only" loopback_only "$listeners"
check "nothing on 0.0.0.0 / [::]" not wildcard_listener "$port"

check "config dir 0700" test "$(stat -c %a /home/$user/.config/hearth)" = 700
check "token 0600" test "$(stat -c %a /home/$user/.config/hearth/token)" = 600
check "db dir 0700" test "$(stat -c %a /home/$user/.local/share/hearth)" = 700
check "db 0600" test "$(stat -c %a /home/$user/.local/share/hearth/hearth.db)" = 600

pid=$(asuser systemctl --user show -p MainPID --value hearthd.service)
check "not running as root" test "$(stat -c %u /proc/"$pid")" = $uid

echo "== first-run browser launch (no display here, so it may fail; it must not be refused by safeexec)"
sleep 1
log=$(journalctl --no-pager -o cat _SYSTEMD_USER_UNIT=hearthd.service 2>/dev/null || true)
echo "$log" | sed 's/^/  log: /'
check "hearthd logged its start" grep -q 'hearthd started' <<<"$log"
check "launch not refused by safeexec allowlist/binary check" not log_has_refusal "$log"
echo "  PrivateUsers=$(asuser systemctl --user show -p PrivateUsers --value hearthd.service)"
echo "  transient units: $(asuser systemctl --user list-units --all --plain --no-legend 'run-*' | awk '{print $1}' | tr '\n' ' ')"

echo "== sandbox actually applied to the running process"
status=$(asuser cat /proc/"$pid"/status)
mounts=$(asuser cat /proc/"$pid"/mountinfo)
check "NoNewPrivs=1" grep -Eq '^NoNewPrivs:\s+1$' <<<"$status"
check "seccomp filter active" grep -Eq '^Seccomp:\s+2$' <<<"$status"
check "/home read-only in its namespace" home_ro "$mounts"
check "~/.local/share/hearth writable" data_rw "$mounts"
check "/usr read-only in its namespace" usr_ro "$mounts"
echo "  uid_map: $(asuser cat /proc/"$pid"/uid_map | tr -s ' ' | tr '\n' ';')"
echo "  journal (xdg-open): $(journalctl --no-pager -o cat 2>/dev/null | grep -m3 -i 'xdg-open\|run-' | tr '\n' ' ')"
asuser systemd-analyze --user security hearthd.service --no-pager 2>/dev/null | tail -1 | sed 's/^/  /' || true
echo "  MemoryCurrent: $(asuser systemctl --user show -p MemoryCurrent --value hearthd.service)"

echo "== purge"
apt-get purge -y -qq hearth >/dev/null
check "package gone" not dpkg -s hearth
for f in /usr/bin/hearthd /usr/lib/systemd/user/hearthd.service \
	/etc/systemd/user/default.target.wants/hearthd.service /usr/share/applications/hearth.desktop; do
	check "removed $f" test ! -e "$f"
done
check "daemon stopped" wait_until 10 not pgrep -x hearthd
check "port closed" not port_open "$port"
check "no /var/lib/hearth" test ! -e /var/lib/hearth

echo
if [ "$failures" -gt 0 ]; then
	echo "itest: $failures check(s) failed"
	exit 1
fi
echo "itest: all checks passed"

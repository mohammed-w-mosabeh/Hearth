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

echo "== broker"
api_key() { curl -s -o /dev/null -w '%{redirect_url}' "http://127.0.0.1:$port/auth?t=$(cat /home/$user/.config/hearth/token)" | sed 's/.*#k=//'; }
broker_status() { curl -s -H "Authorization: Bearer $1" "http://127.0.0.1:$port/api/v1/broker/status"; }
in_group() { id -nG "$user" | tr ' ' '\n' | grep -qx hearth; }
call_says() { # call_says "text" method [params]: broker-call's output contains text
	local want=$1
	shift
	local out
	out=$(asuser /usr/bin/hearthd broker-call "$@" 2>&1 || true) # refusals exit 1; pipefail would hide the match
	grep -q "$want" <<<"$out"
}
check "binary hearth-broker root-owned 0755" test "$(stat -c '%U %a' /usr/bin/hearth-broker)" = "root 755"
check "polkit policy installed" test -f /usr/share/polkit-1/actions/org.hearth.policy
check "group hearth exists" getent group hearth
check "nobody added to hearth automatically" not in_group
check "hearth-broker.socket active" systemctl is-active hearth-broker.socket
check "socket 0660 root:hearth" test "$(stat -c '%a %U %G' /run/hearth/broker.sock)" = "660 root hearth"
check "broker not running before first use" not systemctl is-active hearth-broker.service
key=$(api_key)
echo "  status as non-member: $(broker_status "$key")"
check "status says not a member" grep -q '"reason":"not-member"' <<<"$(broker_status "$key")"
check "non-member cannot call" call_says "not a member" broker.ping
# The socket's file mode refuses a non-member before the broker starts (the
# broker's own SO_PEERCRED group check is the second layer, unit-tested).
check "non-member does not start the broker" not systemctl is-active hearth-broker.service

usermod -aG hearth "$user"
# Group membership applies to new sessions: restart the user manager.
systemctl restart user@$uid.service
wait_until 30 systemctl is-active user@$uid.service || fail "user manager restarted"
asuser systemctl --user start hearthd.service || true
wait_until 15 curl -fsS "http://127.0.0.1:$port/api/v1/health" || fail "hearthd back"
key=$(api_key)
echo "  status as member: $(broker_status "$key")"
check "status says available to a member" grep -q '"member":true' <<<"$(broker_status "$key")"
check "member can ping, seen as uid $uid" call_says "\"uid\":$uid" broker.ping
check "unknown method rejected" call_says "unknown method" exec '{"cmd":"/bin/sh"}'
check "bad params rejected" call_says "takes no params" broker.ping '{"x":1}'
check "privileged call refused without polkit authorization" call_says "not authorized" broker.authorize
bpid=$(pgrep -x hearth-broker || true)
check "broker runs as root" test "$(stat -c %u /proc/"$bpid" 2>/dev/null)" = 0
check "broker has no capabilities" grep -Eq '^CapEff:\s+0+$' /proc/"$bpid"/status
check "broker has no network" not grep -q AF_INET <<<"$(systemctl show -p RestrictAddressFamilies --value hearth-broker.service)"
check "audit log root 0600" test "$(stat -c '%U %a' /var/lib/hearth/audit.jsonl)" = "root 600"
check "audit records the refusals" grep -q 'refused: unknown method' /var/lib/hearth/audit.jsonl
check "audit records polkit refusal" grep -q 'broker.authorize.*refused' /var/lib/hearth/audit.jsonl
systemd-analyze security hearth-broker.service --no-pager 2>/dev/null | tail -1 | sed 's/^/  /' || true
check "broker exits after 60 s idle" wait_until 80 not systemctl is-active hearth-broker.service
check "socket still listening after it exits" systemctl is-active hearth-broker.socket
check "socket starts it again" call_says "broker.ping" broker.ping
# Leave the group empty so purge can remove it.
gpasswd -d "$user" hearth >/dev/null

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
for f in /usr/bin/hearth-broker /usr/lib/systemd/system/hearth-broker.socket \
	/usr/lib/systemd/system/hearth-broker.service /usr/share/polkit-1/actions/org.hearth.policy \
	/etc/systemd/system/sockets.target.wants/hearth-broker.socket /run/hearth; do
	check "removed $f" test ! -e "$f"
done
check "broker stopped" not pgrep -x hearth-broker
check "empty hearth group removed" not getent group hearth

echo
if [ "$failures" -gt 0 ]; then
	echo "itest: $failures check(s) failed"
	exit 1
fi
echo "itest: all checks passed"

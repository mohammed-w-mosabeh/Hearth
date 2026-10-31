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
# Only what disk.smart needs (S16f): CAP_SYS_RAWIO (bit 17), CAP_SYS_ADMIN (21).
capeff=$(awk '/^CapEff:/ {print $2}' /proc/"$bpid"/status)
check "broker holds no capability but RAWIO and SYS_ADMIN" test "$(((0x$capeff) & ~0x220000))" = 0
check "the unit bounds them" test "$(systemctl show -p CapabilityBoundingSet --value hearth-broker.service)" = "cap_sys_rawio cap_sys_admin"
check "the broker opens disks only" test "$(systemctl show -p DevicePolicy --value hearth-broker.service)" = closed
check "broker NoNewPrivs=1" grep -Eq '^NoNewPrivs:\s+1$' /proc/"$bpid"/status
check "broker seccomp filter active" grep -Eq '^Seccomp:\s+2$' /proc/"$bpid"/status
# Between calls the broker holds sockets only: the audit log and the
# Docker socket are opened per call. (The Go runtime keeps its cgroup's
# cpu.max open, read-only, to size GOMAXPROCS.)
echo "  broker fds: $(for f in /proc/"$bpid"/fd/*; do readlink "$f"; done | tr "\n" " ")"
check "broker holds no files between calls" not grep -vE '^(socket|anon_inode):|^/dev/null$|^/sys/fs/cgroup/.*/cpu\.max$' <<<"$(for f in /proc/"$bpid"/fd/*; do readlink "$f"; done)"
policy=/usr/share/polkit-1/actions/org.hearth.policy
check "no polkit action is granted without a password" not grep -Eq '<allow_(any|inactive|active)>yes<' "$policy"
check "exec asks every time (auth_admin, no keep)" grep -Pzq '(?s)org\.hearth\.docker\.exec">.*?<allow_active>auth_admin</allow_active>' "$policy"
check "broker has no network" not grep -q AF_INET <<<"$(systemctl show -p RestrictAddressFamilies --value hearth-broker.service)"
check "audit log root 0600" test "$(stat -c '%U %a' /var/lib/hearth/audit.jsonl)" = "root 600"
check "audit records the refusals" grep -q 'refused: unknown method' /var/lib/hearth/audit.jsonl
check "audit records polkit refusal" grep -q 'broker.authorize.*refused' /var/lib/hearth/audit.jsonl

echo "== broker: docker"
# A stand-in Engine on /run/docker.sock (root only), logging each request.
systemd-run --quiet --unit=fakedocker /root/fakedocker
wait_until 10 test -S /run/docker.sock || fail "fake docker started"
# polkit: the test user may control Docker (not remove) without a password.
cat >/etc/polkit-1/rules.d/50-hearth-itest.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.docker.control" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1 # polkitd reloads rules on change
engine_got() { grep -q "$1" /run/fakedocker.log; }
check "user cannot reach the Docker socket directly" not asuser test -w /run/docker.sock
check "docker.version through the broker" call_says '"ApiVersion":"1.44"' docker.version
check "docker.containers.list" call_says '"/web"' docker.containers.list '{"all":true}'
inspect=$(asuser /usr/bin/hearthd broker-call docker.containers.inspect '{"id":"web"}' 2>&1 || true)
check "inspect masks secret env" grep -q 'DB_PASSWORD=\*\*\*\*\*\*\*\*' <<<"$inspect"
check "inspect never shows the secret" not grep -q hunter2 <<<"$inspect"
check "inspect reports S12 risks" grep -q '"risks":\["privileged","host-mount:/etc"\]' <<<"$inspect"
check "authorized start reaches the engine" call_says '"ok":true' docker.containers.start '{"id":"web"}'
check "engine got the start" engine_got '^POST /v1.44/containers/web/start$'
check "remove refused without polkit authorization" call_says "not authorized" docker.containers.remove '{"id":"web"}'
check "engine never got the remove" not engine_got '^DELETE'
check "no create method" call_says "unknown method" docker.containers.create '{"Image":"alpine","HostConfig":{"Privileged":true}}'
check "path injection refused" call_says "id must be" docker.containers.inspect '{"id":"../../version"}'
check "engine only got allowlisted requests" not grep -vE '^(GET /v1.44/(version|containers/json\?all=true&size=false|containers/web/json)|POST /v1.44/containers/web/start)$' /run/fakedocker.log
check "audit records the docker refusal" grep -q 'docker.containers.remove.*refused: not authorized' /var/lib/hearth/audit.jsonl
echo "  engine log: $(tr '\n' ';' </run/fakedocker.log)"

echo "== broker: pentest probes (test/pentest/brokerprobe, as a member)"
: >/run/fakedocker.log
probe_out=$(asuser /usr/local/bin/hearth-brokerprobe 2>&1) && probe_ok=1 || probe_ok=0
grep -E '^(FAIL|INFO)' <<<"$probe_out" | sed 's/^/  /' || true
echo "  $(grep -c '^PASS' <<<"$probe_out") probes refused as expected"
check "no broker probe got through" test "$probe_ok" = 1
# Probes name only hearth-pentest-nx; anything else reaching the engine
# is an injection that got past validation.
check "audit holds params hashes, never raw params" not grep -Eq 'hearth-pentest-nx|\.\./|detachKeys|LD_PRELOAD' /var/lib/hearth/audit.jsonl
check "audit recorded the privileged refusals" grep -q 'docker.containers.exec.*error: bad params' /var/lib/hearth/audit.jsonl
check "the test user cannot read the audit log" not asuser cat /var/lib/hearth/audit.jsonl
check "engine got nothing but lookups of the probe name" not grep -vE '^GET /v1.44/(containers|images|volumes|networks)/hearth-pentest-nx(/json|/logs\?[a-z=&0-9-]*)?$' /run/fakedocker.log
echo "  engine log: $(tr '\n' ';' </run/fakedocker.log)"

echo "== broker: exec into a risky container (S12a)"
# Authorize exec for the test user, so the refusal tested is the broker's
# own risk check and not polkit's.
cat >/etc/polkit-1/rules.d/51-hearth-itest-exec.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.docker.exec" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1
: >/run/fakedocker.log
check "exec refused in a privileged container with / mounted" call_says "would reach the host (privileged" docker.containers.exec '{"id":"risky"}'
check "engine never got an exec request" not grep -q '/exec' /run/fakedocker.log
rm -f /etc/polkit-1/rules.d/51-hearth-itest-exec.rules
check "volume sizes through the broker, with no polkit" call_says '"name":"orphan","size":2048' docker.volumes.sizes
check "sizes take no params" call_says "takes no params" docker.volumes.sizes '{"path":"/etc"}'
for bad in '{"name":"../x"}' '{"name":"a/b"}' '{"name":"-f"}' '{"name":"orphan","x":1}'; do
	check "volume remove refused before polkit: $bad" call_says "bad params\|must be a volume" docker.volumes.remove "$bad"
done
check "a predefined network is refused" call_says "predefined" docker.networks.remove '{"id":"bridge"}'
check "volume remove refused without polkit authorization" call_says "not authorized" docker.volumes.remove '{"name":"orphan"}'
check "the Engine never got that remove" not grep -q 'DELETE /v1.44/volumes/orphan' /run/fakedocker.log
systemctl stop fakedocker
rm -f /etc/polkit-1/rules.d/50-hearth-itest.rules

echo "== broker: packages"
check "pkg.hold refused without polkit authorization" call_says "not authorized" pkg.hold '{"package":"curl","hold":true}'
check "pkg.hold: bad name refused before polkit" call_says "Debian package name" pkg.hold '{"package":"-o=Dir=/tmp","hold":true}'
check "pkg.hold: extra params refused" call_says "bad params" pkg.hold '{"package":"curl","hold":true,"args":["x"]}'
check "nothing held by refused calls" test -z "$(apt-mark showhold)"
cat >/etc/polkit-1/rules.d/50-hearth-itest.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.packages.hold" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1
check "pkg.hold holds curl through apt-mark" call_says '"held":true' pkg.hold '{"package":"curl","hold":true}'
check "dpkg has curl on hold" grep -qx curl <<<"$(apt-mark showhold)"
check "pkg.hold unholds curl" call_says '"held":false' pkg.hold '{"package":"curl"}'
check "curl no longer held" test -z "$(apt-mark showhold)"
check "pkg.hold on a package that is not installed fails cleanly" call_says "not installed\\|apt-mark" pkg.hold '{"package":"no-such-pkg-xyz","hold":true}'
check "audit records pkg.hold" grep -q '"method":"pkg.hold"' /var/lib/hearth/audit.jsonl
rm -f /etc/polkit-1/rules.d/50-hearth-itest.rules

echo "== broker: cleanups"
arch=/var/cache/apt/archives
mkdir -p $arch /var/log/journal
printf x >$arch/itest.deb
printf x >$arch/itest.txt
ln -sf /etc/hostname $arch/itest-link.deb
check "apt.clean refused without polkit authorization" call_says "not authorized" apt.clean
check "journal.vacuum refused without polkit authorization" call_says "not authorized" journal.vacuum '{"keep_mib":500}'
check "journal.vacuum: other sizes refused before polkit" call_says "keep_mib must be" journal.vacuum '{"keep_mib":1}'
check "apt.clean takes no params" call_says "takes no params" apt.clean '{"dir":"/etc"}'
check "refused calls removed nothing" test -f $arch/itest.deb
cat >/etc/polkit-1/rules.d/50-hearth-itest.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.space.clean" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1
check "apt.clean runs in the broker's sandbox" call_says '"removed":1' apt.clean
check "apt.clean removed the .deb" not test -e $arch/itest.deb
check "apt.clean left other files" test -f $arch/itest.txt
check "apt.clean left the link and its target" test -L $arch/itest-link.deb -a -f /etc/hostname
rm -f $arch/itest.txt $arch/itest-link.deb
check "journal.vacuum runs in the broker's sandbox" call_says '"done":true' journal.vacuum '{"keep_mib":100}'
check "audit records the cleanups" grep -q '"method":"apt.clean"' /var/lib/hearth/audit.jsonl
rm -f /etc/polkit-1/rules.d/50-hearth-itest.rules

exposure=$(systemd-analyze security hearth-broker.service --no-pager 2>/dev/null | tail -1 || true)
echo "  $exposure"
check "broker exposure level at most 2.0" awk -v s="$exposure" 'BEGIN { if (match(s, /: [0-9.]+/)) exit !(substr(s, RSTART + 2, RLENGTH - 2) + 0 <= 2.0); exit 1 }'
check "broker exits after 60 s idle" wait_until 80 not systemctl is-active hearth-broker.service
check "socket still listening after it exits" systemctl is-active hearth-broker.socket
check "socket starts it again" call_says "broker.ping" broker.ping
# Leave the group empty so purge can remove it.
gpasswd -d "$user" hearth >/dev/null

echo "== journal and boot"
key=$(api_key)
jget() { curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/$1"; }
check "journal reads in hearthd's sandbox" test "$(jget 'journal?lines=5')" = 200
check "journal search reads" test "$(jget 'journal?lines=5&text=hearth')" = 200
check "journal refuses a bad unit" test "$(jget 'journal?unit=system/--root=/')" = 400
check "boots listed" test "$(jget journal/boots)" = 200
check "boot analysis answers" test "$(jget boot)" = 200

echo "== packages"
key=$(api_key)
pkgs_api() { curl -s -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/$1"; }
preview_remove() {
	curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' \
		-d "{\"target\":\"$1\",\"preview\":true}" "http://127.0.0.1:$port/api/v1/actions/package/remove"
}
list=$(pkgs_api pkgs)
check "package list read from dpkg inside the sandbox" grep -q '"name":"hearth"' <<<"$list"
check "bash listed as essential" grep -q '"essential":true' <<<"$(grep -o '"name":"bash",[^}]*' <<<"$list")"
echo "  remove hearth: $(preview_remove hearth)"
check "removing Hearth itself is blocked" grep -q '"reason":"self"' <<<"$(preview_remove hearth)"
check "removing bash (Essential) is blocked" grep -q '"reason":"essential"' <<<"$(preview_remove bash)"
check "no token for a blocked removal" not grep -q '"token"' <<<"$(preview_remove bash)"
# No PackageKit in the container: an unprotected package cannot be
# previewed, and says why instead of failing open.
echo "  remove curl without PackageKit: $(preview_remove curl)"
check "without PackageKit a removal is refused" grep -q 'PackageKit' <<<"$(preview_remove curl)"
check "extra managers listed inside the sandbox" grep -q '"manager":"pip","available"' <<<"$(pkgs_api pkgs/pip)"
check "unknown manager refused" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/pkgs/apt-get")" = 404
check "removing a flatpak that is not installed is refused" grep -q 'not installed' <<<"$(curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" \
	-d '{"target":"system/app/org.example.App/x86_64/stable","preview":true}' "http://127.0.0.1:$port/api/v1/actions/flatpak/remove")"

detail=$(pkgs_api "pkgs/apt/bash:$(dpkg --print-architecture)")
check "package detail lists bash's files" grep -q '"/usr/bin/bash"\|"/bin/bash"' <<<"$detail"
check "package detail lists installed reverse deps" grep -q '"reverse_deps":\[' <<<"$detail"
check "detail of a missing package is 404" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/pkgs/apt/nope:amd64")" = 404
echo "  updates without PackageKit: $(pkgs_api pkgs/updates)"
check "without PackageKit updates say so" grep -q 'PackageKit' <<<"$(pkgs_api pkgs/updates)"
hold_preview() {
	curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" \
		-d "{\"target\":\"$1\",\"preview\":true}" "http://127.0.0.1:$port/api/v1/actions/package/hold"
}
check "hold preview names the package" grep -q '"name":"curl"' <<<"$(hold_preview curl)"
check "hold of a package that is not installed is refused" grep -q 'not installed' <<<"$(hold_preview no-such-pkg-xyz)"

echo "== tools"
# A canary in the user's PATH: if Hearth ever ran a listed program, it
# would leave a marker where hearthd's sandbox can write.
asuser mkdir -p /home/$user/.local/bin
cat >/home/$user/.local/bin/hearth-canary <<'CANARY'
#!/bin/sh
touch "$HOME/.config/hearth/canary-ran"
CANARY
chmod 0755 /home/$user/.local/bin/hearth-canary
chown "$user:$user" /home/$user/.local/bin/hearth-canary
tools=$(pkgs_api tools)
tool_of() { grep -o "{\"name\":\"$1\",[^}]*}" <<<"$tools"; }
echo "  bash: $(tool_of bash)"
check "tools list bash owned by package bash" grep -q '"manager":"apt","package":"bash"' <<<"$(tool_of bash)"
check "bash has its man page summary" grep -q '"summary":"GNU Bourne-Again SHell"' <<<"$(tool_of bash)"
check "canary listed, not from a package" test -n "$(tool_of hearth-canary)"
check "no listed program was run" test ! -e /home/$user/.config/hearth/canary-ran
check "man page of curl shown" grep -q 'transfer a URL' <<<"$(pkgs_api 'tools/man?name=curl')"
check "man of an option is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/tools/man?name=-H")" = 400
check "man of something not listed is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/tools/man?name=nonexistent-tool")" = 404
check "no listed program was run (after man)" test ! -e /home/$user/.config/hearth/canary-ran

echo "== network"
# A listener of another user (nobody), on every address, in its own unit:
# hearthd cannot read its fds, so it is attributed through its cgroup.
systemd-run --quiet --unit=itest-listen -p User=nobody /usr/bin/systemd-socket-activate -l 0.0.0.0:7777 /bin/cat
listening() { ss -ltn | grep -q ":$1 "; }
wait_until 5 listening 7777 || true
socks=$(pkgs_api net/sockets)
listener_on() { grep -oE "\{\"proto\":\"tcp\",\"address\":\"[^\"]*\",\"port\":$1,[^{}]*(\{[^{}]*\}[^{}]*)?\}" <<<"$socks" | head -1; }
echo "  7777: $(listener_on 7777)"
check "other user's listener on 0.0.0.0 is exposed" grep -q '"scope":"all"' <<<"$(listener_on 7777)"
check "its user is nobody" grep -q '"user":"nobody"' <<<"$(listener_on 7777)"
check "attributed to its unit through the cgroup" grep -q '"unit":"itest-listen.service"' <<<"$(listener_on 7777)"
check "but not as an exact process" not grep -q '"exact":true' <<<"$(listener_on 7777)"
echo "  hearthd: $(listener_on "$port")"
check "hearthd's own listener is loopback only" grep -q '"address":"127.0.0.1","port":'"$port"',"scope":"loopback"' <<<"$socks"
check "hearthd's own listener has its exact process" grep -q '"exact":true,"group":"user-unit:hearthd.service"' <<<"$(listener_on "$port")"
listener_alert() { pkgs_api alerts/events | grep -q '"net.new_listener"[^}]*TCP 7777\|TCP 7777[^}]*"net.new_listener"'; }
check "the new listener on 0.0.0.0 raised an alert (polled every 60 s)" wait_until 75 listener_alert
check "hearthd's own (loopback) listener did not" not grep -q "TCP $port " <<<"$(pkgs_api alerts/events)"
lineage=$(pkgs_api 'net/lineage?scope=user&unit=hearthd.service')
echo "  hearthd lineage: $lineage"
check "lineage: hearthd's unit file is from package hearth" grep -q '"unit_package":{"name":"hearth"' <<<"$lineage"
check "lineage: its program is /usr/bin/hearthd from hearth" grep -q '"exe":"/usr/bin/hearthd","exe_package":{"name":"hearth"' <<<"$lineage"
lineage=$(pkgs_api 'net/lineage?scope=system&unit=itest-listen.service')
check "lineage: a transient unit's file has no package" not grep -q '"unit_package"' <<<"$lineage"
check "lineage: but its program comes from systemd" grep -q '"exe":"/usr/bin/systemd-socket-activate","exe_package":{"name":"systemd"' <<<"$lineage"
check "lineage: an option as unit is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/net/lineage?scope=system&unit=-H")" = 400
systemctl stop itest-listen.service
state=$(pkgs_api net/state)
check "network state lists lo" grep -q '"name":"lo","kind":"loopback"' <<<"$state"
check "network state reports the firewalls" grep -q '"firewalls":\[{"name":"ufw"' <<<"$state"
check "NetworkManager absent is reported" grep -q '"nm":{"running":false' <<<"$state"

echo "== scheduler (user jobs)"
post() { curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d "$2" "http://127.0.0.1:$port/api/v1/$1"; }
check "schedule check: calendar" grep -q '"valid":true,"normalized":"Mon..Fri \*-\*-\* 09:30:00"' <<<"$(pkgs_api 'jobs/check?kind=calendar&value=Mon..Fri%2009:30')"
check "schedule check: nonsense refused" grep -q '"valid":false' <<<"$(pkgs_api 'jobs/check?kind=calendar&value=bogus')"
saved=$(post jobs/user '{"name":"itest 50% job","script":"#!/bin/sh\necho job-said-$((6*7))\nexit 3\n","timeout":30,"notify":true,"schedule":{"kind":"calendar","value":"*-*-* 03:00"}}')
jid=$(sed -nE 's/.*"id":"([a-f0-9]{8})".*/\1/p' <<<"$saved")
echo "  job: ${jid:-none} $saved"
check "job saved" test -n "$jid"
check "its units are in ~/.config/systemd/user" test -f "/home/$user/.config/systemd/user/hearth-job-$jid.timer"
check "its script is the user's, mode 0700" test "$(stat -c '%U %a' /home/$user/.local/share/hearth/jobs/$jid/run)" = "$user 700"
check "systemd accepts the units" asuser systemd-analyze --user verify "/home/$user/.config/systemd/user/hearth-job-$jid.service"
check "its timer is active" asuser systemctl --user is-active "hearth-job-$jid.timer"
check "listed with its next run" grep -q "\"id\":\"$jid\",\"name\":\"itest 50% job\".*\"paused\":false.*\"next\":[0-9]" <<<"$(pkgs_api jobs)"
check "run now" grep -q '"done":1' <<<"$(post actions/job/run "{\"target\":\"user/$jid\"}")"
ran() { asuser systemctl --user is-failed "hearth-job-$jid.service"; }
wait_until 10 ran || true
hist=$(pkgs_api "jobs/user/$jid/history")
echo "  history: $hist"
check "history has the failed run with its exit code and output" grep -q '"result":"failed","exit":3,"output":\["job-said-42"\]' <<<"$hist"
check "listed as failed" grep -q "\"id\":\"$jid\".*\"failed\":true" <<<"$(pkgs_api jobs)"
job_alert() { pkgs_api alerts/events | grep -q '"job.failed".*itest 50% job\|itest 50% job.*"job.failed"'; }
check "its failure raised a job.failed alert (failed units are polled every 30 s)" wait_until 45 job_alert
check "and not a unit.failed one" not grep -q "unit.failed[^}]*hearth-job-$jid" <<<"$(pkgs_api alerts/events)"
check "pause" grep -q '"done":1' <<<"$(post actions/job/pause "{\"target\":\"user/$jid\"}")"
check "paused timer is stopped" not asuser systemctl --user is-active "hearth-job-$jid.timer"
token=$(post actions/job/delete "{\"target\":\"user/$jid\",\"preview\":true}" | sed -nE 's/.*"token":"([^"]+)".*/\1/p')
check "delete needs its preview" grep -q '"done":1' <<<"$(post actions/job/delete "{\"target\":\"user/$jid\",\"token\":\"$token\"}")"
check "units and script removed" test ! -e "/home/$user/.config/systemd/user/hearth-job-$jid.timer" -a ! -e "/home/$user/.local/share/hearth/jobs/$jid"
check "a schedule with a newline is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -d '{"name":"x","script":"#!/bin/sh\n","timeout":5,"schedule":{"kind":"calendar","value":"daily\nExecStartPre=/bin/false"}}' "http://127.0.0.1:$port/api/v1/jobs/user")" = 400
check "no unit was written for it" test -z "$(ls /home/$user/.config/systemd/user | grep hearth-job-)"

echo "== scheduler (root jobs, through hearth-broker)"
# The broker checks the group database; hearthd kept the group it started
# with, so its socket connection works.
usermod -aG hearth "$user"
rootjob='{"name":"itest root job","script":"#!/bin/sh\nid -un\n","timeout":30,"notify":false,"schedule":{"kind":"interval","value":"6h"}}'
check "root job refused without polkit authorization" grep -q 'not authorized\|unauthorized' <<<"$(post jobs/system "$rootjob")"
check "nothing written when refused" test -z "$(ls /etc/systemd/system | grep hearth-job-)"
cat >/etc/polkit-1/rules.d/51-hearth-itest-jobs.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.jobs.manage" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1 # polkitd reloads rules on change
saved=$(post jobs/system "$rootjob")
rid=$(sed -nE 's/.*"id":"([a-f0-9]{8})".*/\1/p' <<<"$saved")
echo "  root job: ${rid:-none} $saved"
check "root job saved" test -n "$rid"
check "its script is root's, 0755, in /var/lib/hearth/jobs" test "$(stat -c '%U %a' /var/lib/hearth/jobs/$rid/run)" = "root 755"
check "its units are root's in /etc/systemd/system" test "$(stat -c '%U %a' /etc/systemd/system/hearth-job-$rid.timer)" = "root 644"
check "systemd accepts the units" systemd-analyze verify "/etc/systemd/system/hearth-job-$rid.service"
check "its timer is active" systemctl is-active "hearth-job-$rid.timer"
check "listed as a system job" grep -q "\"id\":\"$rid\",\"name\":\"itest root job\".*\"scope\":\"system\"" <<<"$(pkgs_api jobs)"
check "its script is shown through the broker" grep -q '"script":"#!/bin/sh\\nid -un\\n"' <<<"$(pkgs_api "jobs/system/$rid")"
check "run now" grep -q '"done":1' <<<"$(post actions/job/run "{\"target\":\"system/$rid\"}")"
rootran() { systemctl show -p Result --value "hearth-job-$rid.service" | grep -q success && test "$(systemctl show -p ExecMainExitTimestampMonotonic --value "hearth-job-$rid.service")" != 0; }
check "it ran as root" wait_until 10 rootran
check "and said root" grep -q '^root$' <<<"$(journalctl -q -o cat -u "hearth-job-$rid.service")"
token=$(post actions/job/delete "{\"target\":\"system/$rid\",\"preview\":true}" | sed -nE 's/.*"token":"([^"]+)".*/\1/p')
check "deleted" grep -q '"done":1' <<<"$(post actions/job/delete "{\"target\":\"system/$rid\",\"token\":\"$token\"}")"
check "root units and script removed" test ! -e "/etc/systemd/system/hearth-job-$rid.timer" -a ! -e "/var/lib/hearth/jobs/$rid"
check "broker audited the job calls" grep -q 'sched.save.*ok' /var/lib/hearth/audit.jsonl

echo "== service limits and masking"
printf '[Service]\nExecStart=/bin/sleep 600\n' >/etc/systemd/system/itest-limits.service
printf '[Service]\nExecStart=/bin/sleep 600\n' >/etc/systemd/user/itest-user.service
systemctl daemon-reload
asuser systemctl --user daemon-reload
original=$(sha256sum /etc/systemd/system/itest-limits.service)
dropdir=/etc/systemd/system/itest-limits.service.d
for bad in '"ExecStart":"/bin/sh"' '"ExecStartPre":"/bin/sh"' '"Environment":"LD_PRELOAD=/tmp/x"' '"User":"root"' '"WorkingDirectory":"/root"' '"ReadWritePaths":"/"' '"MemoryMax":"1G\nExecStart=/bin/sh"'; do
	check "refused before polkit: $bad" call_says "$(test "${bad:1:9}" = MemoryMax && echo 'bad value' || echo 'not allowed')" unit.override "{\"unit\":\"itest-limits.service\",\"directives\":{$bad}}"
done
check "another file is unreachable" call_says "unit" unit.override '{"unit":"../../cron.d/x.service","directives":{"Nice":"1"}}'
check "Hearth's own units are refused" call_says "keep their limits" unit.override '{"unit":"hearth-broker.service","directives":{"Nice":"1"}}'
check "refused without polkit authorization" call_says "not authorized" unit.override '{"unit":"itest-limits.service","directives":{"Nice":"1"}}'
check "nothing written when refused" test ! -e "$dropdir"
check "override asks every time (auth_admin, no keep)" grep -Pzq '(?s)org\.hearth\.units\.override">.*?<allow_active>auth_admin</allow_active>' "$policy"
cat >/etc/polkit-1/rules.d/52-hearth-itest-units.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.units.override" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1 # polkitd reloads rules on change
limits='{"target":"system/itest-limits.service","params":{"MemoryMax":"512M","Restart":"on-failure"}}'
check "system unit limited through the broker" grep -q '"done":1' <<<"$(post actions/unit/override "$limits")"
check "the drop-in is root's, 0644" test "$(stat -c '%U %a' "$dropdir/hearth.conf")" = "root 644"
check "its directory is readable by hearthd" test "$(stat -c '%a' "$dropdir")" = 755
check "systemd reloaded and applied it" test "$(systemctl show -p MemoryMax --value itest-limits.service)" = 536870912
check "the original unit is untouched" test "$original" = "$(sha256sum /etc/systemd/system/itest-limits.service)"
check "shown back to the UI" grep -q '"MemoryMax":"512M"' <<<"$(pkgs_api services/system/itest-limits.service/override)"
check "a directive outside the allowlist is refused by hearthd" test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d '{"target":"system/itest-limits.service","params":{"ExecStart":"/bin/sh"}}' "http://127.0.0.1:$port/api/v1/actions/unit/override")" = 400
check "broker audited it" grep -q 'unit.override.*ok' /var/lib/hearth/audit.jsonl
check "hearthd audited it" grep -q 'unit.override' <<<"$(pkgs_api changes)"
check "user unit limited by hearthd" grep -q '"done":1' <<<"$(post actions/unit/override '{"target":"user/itest-user.service","params":{"CPUQuota":"50%"}}')"
check "its drop-in is in the user's folder" grep -q '^CPUQuota=50%$' "/home/$user/.config/systemd/user/itest-user.service.d/hearth.conf"
check "the user manager applied it" test "$(asuser systemctl --user show -p CPUQuotaPerSecUSec --value itest-user.service)" = 500ms
check "clearing removes it" grep -q '"done":1' <<<"$(post actions/unit/override '{"target":"user/itest-user.service","params":{}}')"
check "and its folder" test ! -e "/home/$user/.config/systemd/user/itest-user.service.d"
check "mask" grep -q '"done":1' <<<"$(post actions/unit/mask '{"target":"user/itest-user.service"}')"
check "masked" grep -q masked <<<"$(asuser systemctl --user is-enabled itest-user.service 2>&1 || true)"
check "a masked unit cannot start" not asuser systemctl --user start itest-user.service
check "unmask" grep -q '"done":1' <<<"$(post actions/unit/unmask '{"target":"user/itest-user.service"}')"
check "unmasked" not grep -q masked <<<"$(asuser systemctl --user is-enabled itest-user.service 2>&1 || true)"
check "Hearth's own unit cannot be masked" grep -q '"reason":"self"' <<<"$(post actions/unit/mask '{"target":"user/hearthd.service"}')"
rm -f /etc/polkit-1/rules.d/51-hearth-itest-jobs.rules
gpasswd -d "$user" hearth >/dev/null

echo "== settings"
info=$(pkgs_api system/info)
echo "  info: ${info:0:200}"
check "system info: hostname from hostnamed" grep -q '"static":"' <<<"$info"
check "system info: time zones from timedated" grep -q '"timezones":\["' <<<"$(pkgs_api system/timezones)"
check "network: answers without NetworkManager" grep -q '"available":false' <<<"$(pkgs_api system/network)"
mkdir -p /etc/xdg/autostart
printf '[Desktop Entry]\nType=Application\nName=itest autostart\nExec=/bin/true\n' >/etc/xdg/autostart/hearth-itest.desktop
check "autostart: system entry listed as on" grep -q '"id":"hearth-itest","name":"itest autostart","enabled":true,"origin":"system"' <<<"$(pkgs_api system/autostart)"
check "autostart: turned off" grep -q '"done":1' <<<"$(post actions/autostart/set '{"target":"hearth-itest","params":{"enabled":"false"}}')"
check "autostart: through the sandbox, into ~/.config/autostart" grep -qx 'Hidden=true' "/home/$user/.config/autostart/hearth-itest.desktop"
check "autostart: the system file is untouched" not grep -q Hidden /etc/xdg/autostart/hearth-itest.desktop
check "autostart: listed as off" grep -q '"id":"hearth-itest","name":"itest autostart","enabled":false' <<<"$(pkgs_api system/autostart)"
check "autostart: a path is refused" not grep -q '"done":1' <<<"$(post actions/autostart/set '{"target":"../../.bashrc","params":{"enabled":"false"}}')"
hn=$(hostname)
check "hostname: a malformed name is refused before D-Bus" grep -q 'hostname is letters' <<<"$(post actions/system/hostname '{"target":"static","params":{"value":"Bad Name"}}')"
check "hostname: unchanged" test "$(hostname)" = "$hn"
check "power off needs a preview token" not grep -q '"done":1' <<<"$(post actions/power/poweroff '{"target":"machine"}')"
rm -f /etc/xdg/autostart/hearth-itest.desktop "/home/$user/.config/autostart/hearth-itest.desktop"

echo "== terminal"
# A shell through hearthd: it must run as the user, in the folder asked,
# outside hearthd's sandbox (home writable), on a terminal of its own.
term() { asuser hearth-termprobe -url "ws://127.0.0.1:$port/api/v1/terminal?$1" -origin "http://127.0.0.1:$port" -key "$key" -send "$2"; }
out=$(term 'cols=100&rows=30&dir=/usr/share' 'echo "R=$((6*7)) U=$(id -un) D=$PWD C=$COLUMNS"; touch ~/term-wrote && echo W=ok; tty -s && echo T=tty; exit
')
echo "$out" | tail -n 8 | sed 's/^/  | /'
check "terminal: shell ready" grep -q '"state":"ready"' <<<"$out"
check "terminal: runs as the user, in the folder, at the size asked" grep -q "R=42 U=$user D=/usr/share C=100" <<<"$out"
check "terminal: outside hearthd's sandbox (home writable)" grep -q 'W=ok' <<<"$out"
check "terminal: has its own terminal" grep -q 'T=tty' <<<"$out"
check "terminal: exit is reported" grep -q '"state":"exit"' <<<"$out"
check "terminal: its unit is gone" test -z "$(asuser systemctl --user list-units --plain --no-legend 'hearth-term-*' | grep active)"
check "terminal: a relative folder is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/terminal?dir=tmp")" = 400
check "terminal: a foreign Origin is refused" grep -q 'refused: 403' <<<"$(asuser hearth-termprobe -url "ws://127.0.0.1:$port/api/v1/terminal" -origin http://evil.test -key "$key" || true)"

echo "== e2e: XSS (Playwright, Debian's chromium)"
# Hostile strings wherever the UI shows system-derived text: a container
# (name, image, status, compose project), a package description, a
# listening process's name, and a tile, a job and an alert rule.
usermod -aG hearth "$user"
touch /run/fakedocker.hostile
systemd-run --quiet --unit=fakedocker /root/fakedocker
wait_until 10 test -S /run/docker.sock || fail "fake docker started again"
xss='<img src=x onerror="window.__xss=1">'
mkdir -p /tmp/xssdeb/DEBIAN
printf 'Package: hearth-xss-probe\nVersion: 1.0\nArchitecture: all\nMaintainer: itest <itest@example.invalid>\nDescription: %s\n %s\n' "$xss" "$xss" >/tmp/xssdeb/DEBIAN/control
dpkg-deb --build /tmp/xssdeb /tmp/hearth-xss-probe.deb >/dev/null && dpkg -i /tmp/hearth-xss-probe.deb >/dev/null
check "hostile package installed" dpkg -s hearth-xss-probe
# In a scope of its own: hearthd's user namespace cannot read the fds of
# processes outside it, so the owner comes from the socket's cgroup, and a
# scope is named after its process.
cp /usr/bin/systemd-socket-activate '/tmp/<svg onload>'
systemd-run --quiet --scope --unit=itest-xss-listen --uid="$user" '/tmp/<svg onload>' -l 0.0.0.0:7778 /bin/cat >/dev/null 2>&1 &
check "hostile-named listener up" wait_until 5 listening 7778
socks=$(pkgs_api net/sockets)
echo "  7778: $(listener_on 7778)"
check "named after its process" grep -q '"group_name":"\\u003csvg onload\\u003e"\|"group_name":"<svg onload>"' <<<"$(listener_on 7778)"
# An autostart entry named with the payload (Settings page).
mkdir -p /etc/xdg/autostart
printf '[Desktop Entry]\nType=Application\nName=%s\nComment=%s\nExec=/bin/true\n' "$xss" "$xss" >/etc/xdg/autostart/hearth-xss.desktop
# A cache folder named with the payload (Space advisor).
asuser mkdir -p "/home/$user/.cache/$xss"
# A Steam game whose name is the payload (VDF escapes its quotes).
steamapps=/home/$user/.local/share/Steam/steamapps
asuser mkdir -p "$steamapps"
printf '"AppState"\n{\n\t"appid"\t"9"\n\t"name"\t"%s"\n\t"StateFlags"\t"4"\n}\n' "${xss//\"/\\\"}" | asuser tee "$steamapps/appmanifest_9.acf" >/dev/null
check "hostile game listed" grep -q '"id":"steam.9"' <<<"$(pkgs_api games)"
e2e=$(cd /opt/e2e && HEARTH_VERSION="$HEARTH_VERSION" HEARTH_URL="http://127.0.0.1:$port/auth?t=$(cat /home/$user/.config/hearth/token)" timeout 300 npx playwright test 2>&1) || true
if grep -qE '[0-9]+ passed' <<<"$e2e" && ! grep -qE '[0-9]+ (failed|flaky)' <<<"$e2e"; then
	echo "$e2e" | grep -E '✓|passed' | head -20 | sed 's/^/  | /'
else
	echo "$e2e" | tail -120 | sed 's/^/  | /'
fi
check "e2e XSS: every page shows the strings as text, nothing ran" grep -qE '^ *[0-9]+ passed' <<<"$e2e"
check "e2e XSS: no test failed" not grep -qE '[0-9]+ failed' <<<"$e2e"
pkill -f '/tmp/<svg onload>' || true
dpkg -r hearth-xss-probe >/dev/null
rm -r "/home/$user/.local/share/Steam"
rm -f /etc/xdg/autostart/hearth-xss.desktop
systemctl stop fakedocker
rm -f /run/fakedocker.hostile '/tmp/<svg onload>'
gpasswd -d "$user" hearth >/dev/null

echo "== e2e: DNS rebinding"
for h in evil.test 127.0.0.1.evil.test "localhost:$((port + 1))" "[::1]:$port"; do
	check "Host $h refused (page)" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $h" "http://127.0.0.1:$port/")" = 403
	check "Host $h refused (API with a valid key)" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $h" -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/jobs")" = 403
done
check "Host evil.test refused (WebSocket upgrade)" test "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.test' -H "Origin: http://127.0.0.1:$port" -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H "Sec-WebSocket-Protocol: hearth.v1, hearth.key.$key" "http://127.0.0.1:$port/api/v1/terminal")" = 403
check "a rebound page's Origin cannot open a terminal" grep -q 'refused: 403' <<<"$(asuser hearth-termprobe -url "ws://127.0.0.1:$port/api/v1/terminal" -origin "http://evil.test:$port" -key "$key" || true)"

echo "== resource caps"
asuser systemd-run --user --quiet --unit=itest-cap.service /bin/sleep 300
check "a user unit is capped" grep -q '"done":1' <<<"$(post actions/unit/cap '{"target":"user/itest-cap.service","params":{"MemoryMax":"64M","CPUQuota":"50%"}}')"
check "memory applied" test "$(asuser systemctl --user show -p MemoryMax --value itest-cap.service)" = 67108864
check "CPU applied" test "$(asuser systemctl --user show -p CPUQuotaPerSecUSec --value itest-cap.service)" = 500ms
check "nothing written to the unit folder" test -z "$(ls /home/$user/.config/systemd/user | grep itest-cap || true)"
check "clearing removes the cap" grep -q '"done":1' <<<"$(post actions/unit/cap '{"target":"user/itest-cap.service","params":{}}')"
check "no memory limit left" test "$(asuser systemctl --user show -p MemoryMax --value itest-cap.service)" = infinity
check "a critical unit is refused" grep -q '"reason":"critical"' <<<"$(post actions/unit/cap '{"target":"user/dbus.service","params":{"MemoryMax":"64M"}}')"
check "a system unit is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d '{"target":"system/cron.service","params":{"MemoryMax":"64M"}}' "http://127.0.0.1:$port/api/v1/actions/unit/cap")" = 400
sleeper=$(asuser systemctl --user show -p MainPID --value itest-cap.service)
started=$(awk '{print $22}' "/proc/$sleeper/stat")
check "disk priority set" grep -q '"done":1' <<<"$(post actions/proc/ionice "{\"target\":\"pid:$sleeper:$started\",\"params\":{\"class\":\"idle\"}}")"
check "it is idle" grep -q idle <<<"$(ionice -p "$sleeper")"
check "the realtime class is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d "{\"target\":\"pid:$sleeper:$started\",\"params\":{\"class\":\"realtime\",\"level\":\"0\"}}" "http://127.0.0.1:$port/api/v1/actions/proc/ionice")" = 400
asuser systemctl --user stop itest-cap.service
# Broker access again: the e2e section took both away.
usermod -aG hearth "$user"
systemd-run --quiet --unit=fakedocker /root/fakedocker
wait_until 10 test -S /run/docker.sock || fail "fake docker started for limits"
cat >/etc/polkit-1/rules.d/50-hearth-itest.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.docker.control" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1 # polkitd reloads rules on change
for bad in '{"id":"web","privileged":true}' '{"id":"web","CapAdd":["ALL"]}' '{"id":"web","memory":1}' '{"id":"web","restart":"sometimes"}' '{"id":"web"}'; do
	check "update refused before polkit: $bad" call_says "bad params\|memory must\|restart must\|nothing to update" docker.containers.update "$bad"
done
check "a container limit goes through the broker" call_says '"ok":true' docker.containers.update '{"id":"web","memory":67108864,"nano_cpus":500000000,"restart":"always"}'
check "the Engine saw it" grep -q 'POST /v1.44/containers/web/update' /run/fakedocker.log
check "broker audited it" grep -q 'docker.containers.update.*ok' /var/lib/hearth/audit.jsonl
systemctl stop fakedocker
rm -f /etc/polkit-1/rules.d/50-hearth-itest.rules

echo "== disk health"
disk=$(for d in /sys/block/*; do n=${d##*/}; [[ $n =~ ^(sd[a-z]+|nvme[0-9]+n[0-9]+)$ ]] && [ -e "$d/device" ] && echo "$n" && break; done || true)
check "without smartmontools the broker says so" test -z "$disk" -o -n "$(call_says 'smartmontools is not installed' disk.smart "{\"device\":\"$disk\"}" && echo y)"
check "the default alert exists" grep -q '"metric":"disk.smart_failing"' <<<"$(pkgs_api alerts/rules)"
for bad in '{"device":"sda1"}' '{"device":"../../etc/shadow"}' '{"device":"/dev/sda"}' '{"device":"-a"}' '{"device":"loop0"}' '{"device":"sda","x":1}'; do
	check "disk.smart refused: $bad" call_says "bad params\|whole disk" disk.smart "$bad"
done
for bad in '{"device":"sda1","kind":"short"}' '{"device":"sda","kind":"offline"}' '{"device":"sda","kind":"short","force":true}'; do
	check "disk.selftest refused before polkit: $bad" call_says "bad params\|whole disk\|kind must\|no such disk" disk.selftest "$bad"
done
check "self-test policy is present" grep -q 'org.hearth.disk.selftest' "$policy"
check "the health route answers" grep -q '"status":' <<<"$(pkgs_api disks/health)"
if [ -n "$disk" ]; then
	check "disk.selftest refused without polkit authorization" call_says "not authorized" disk.selftest "{\"device\":\"$disk\",\"kind\":\"short\"}"
	# A stand-in smartctl: this container has no disk the broker may open.
	install -m 0755 /dev/stdin /usr/sbin/smartctl <<'FAKE'
#!/bin/bash
echo "$*" >>/var/lib/hearth/fakesmart.log
echo '{"device":{"protocol":"ATA"},"model_name":"<b>Fake</b>","serial_number":"SERIALSECRET","smart_status":{"passed":false},"temperature":{"current":33},"power_on_time":{"hours":48}}'
exit 8
FAKE
	check "a read goes through the broker" call_says '"failing":true' disk.smart "{\"device\":\"$disk\"}"
	check "the serial stays in the broker" not call_says SERIALSECRET disk.smart "{\"device\":\"$disk\"}"
	check "smartctl got the fixed argv" grep -qx -- "-j -a -n standby /dev/$disk" /var/lib/hearth/fakesmart.log
	check "broker audited it" grep -q 'disk.smart.*ok' /var/lib/hearth/audit.jsonl
	rm -f /usr/sbin/smartctl /var/lib/hearth/fakesmart.log
fi
gpasswd -d "$user" hearth >/dev/null

echo "== alert channels and log alerts"
put_channels() { curl -s -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d "$1" "http://127.0.0.1:$port/api/v1/alerts/channels"; }
check "ntfy over plain HTTP is refused" test "$(put_channels '{"ntfy":{"enabled":true,"server":"http://ntfy.example","topic":"t"}}')" = 400
check "mail without TLS is refused" test "$(put_channels '{"smtp":{"enabled":true,"host":"mail.example","port":25,"security":"none","from":"a@b.c","to":"d@e.f"}}')" = 400
check "a channel saves" test "$(put_channels '{"ntfy":{"server":"https://ntfy.example","topic":"itest-topic-secret","token":"itest-token-secret"}}')" = 200
check "the API never returns the secrets" not grep -q 'itest-.*-secret' <<<"$(pkgs_api alerts/channels)"
check "channels file is 0600" test "$(stat -c '%a' "/home/$user/.config/hearth/channels.json")" = 600
db="/home/$user/.local/share/hearth/hearth.db"
check "the secrets are not in the database" not grep -aq 'itest-token-secret' "$db" "$db-wal"
check "nor in the audit log" not grep -q 'itest-token-secret' <<<"$(pkgs_api changes)"
check "a hostile pattern is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d '{"name":"x","metric":"log.match","target":".*","op":">","threshold":0,"for_s":0,"severity":"info","enabled":true}' "http://127.0.0.1:$port/api/v1/alerts/rules")" = 400
post alerts/rules '{"name":"itest log","metric":"log.match","target":"itest-[a-z]+-marker","op":">","threshold":0,"for_s":0,"severity":"info","enabled":true}' >/dev/null
log_alert() { asuser logger "itest-log-marker password=hunter2"; grep -q '"rule_name":"itest log"' <<<"$(pkgs_api 'alerts/events?limit=20')"; }
check "a matching journal line opens an incident" wait_until 100 log_alert
check "the line itself is not recorded" not grep -q hunter2 <<<"$(pkgs_api 'alerts/events?limit=20')"

echo "== restart needed"
check "a clean machine has no reboot flag" grep -q '"reboot":false,"flagged":false,"running":"' <<<"$(pkgs_api restart)"
touch /run/reboot-required
echo libc6 >/run/reboot-required.pkgs
flagged=$(pkgs_api restart)
echo "  flagged: ${flagged:0:200}"
check "the reboot flag is seen" grep -q '"reboot":true,"flagged":true,"packages":\["libc6"\]' <<<"$flagged"
rm -f /run/reboot-required /run/reboot-required.pkgs
# A service that keeps a deleted library mapped: real needrestart must find
# it, which it can only do with CAP_SYS_PTRACE (another uid's maps).
usermod -aG hearth "$user"
cp -L /usr/lib/x86_64-linux-gnu/libz.so.1 /usr/lib/itest-old.so
systemd-run --quiet --unit=itest-oldlib.service -p DynamicUser=yes -p Environment=LD_PRELOAD=/usr/lib/itest-old.so /bin/sleep 600
wait_until 5 systemctl is-active itest-oldlib.service || true
sleep 1
rm -f /usr/lib/itest-old.so /var/lib/hearth/needrestart.out
nr=$(asuser /usr/bin/hearthd broker-call system.needrestart 2>&1 || true)
echo "  needrestart: ${nr:0:200}"
check "real needrestart ran in its sandbox" grep -q '"installed":true' <<<"$nr"
check "and found the service on a deleted library" grep -q 'itest-oldlib.service' <<<"$nr"
check "the broker gained no capability for it" test "$(systemctl show -p CapabilityBoundingSet --value hearth-broker.service)" = "cap_sys_rawio cap_sys_admin"
check "it takes no params" call_says "takes no params" system.needrestart '{"args":"-r a"}'
check "it restarted nothing" systemctl is-active itest-oldlib.service
check "its output is root's" test "$(stat -c '%U %a' /var/lib/hearth/needrestart.out)" = "root 600"
exposure2=$(systemd-analyze security hearth-needrestart.service --no-pager 2>/dev/null | tail -1 || true)
echo "  $exposure2"
systemctl stop itest-oldlib.service
gpasswd -d "$user" hearth >/dev/null

echo "== automatic updates"
security_has() { pkgs_api security | grep -q "\"code\":\"$1\""; }
rm -f /etc/apt/apt.conf.d/20auto-upgrades
check "installed but not configured is off" security_has unattended.off
cat >/etc/apt/apt.conf.d/20auto-upgrades <<'CONF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
CONF
mkdir -p /var/lib/apt/periodic
touch /var/lib/apt/periodic/unattended-upgrades-stamp
systemctl start apt-daily-upgrade.timer
check "the real settings and stamp read as on" security_has unattended.active
touch -d '30 days ago' /var/lib/apt/periodic/unattended-upgrades-stamp
check "an old stamp is overdue" security_has unattended.stale
rm -f /etc/apt/apt.conf.d/20auto-upgrades /var/lib/apt/periodic/unattended-upgrades-stamp
systemctl stop apt-daily-upgrade.timer

echo "== launcher"
check "hearthd --version is the package's version" test "$(asuser /usr/bin/hearthd --version)" = "$(dpkg-query -W -f='${Version}' hearth)"
check "and git describe's" test "$(asuser /usr/bin/hearthd --version)" = "$HEARTH_VERSION"
units_before=$(asuser systemctl --user list-units --all --plain --no-legend 'run-*' | awk '{print $1}')
tokenfile=/home/$user/.config/hearth/token
stublog=/home/$user/stub-browser.log
portallog=/home/$user/portal.log
opener() { asuser env HOME="/home/$user" /usr/bin/hearthd open; }
cat >/usr/local/bin/hearth-stub-browser <<'STUB'
#!/bin/sh
curl -s -o /dev/null -w '%{http_code}\n' "$1" >>"$HOME/stub-browser.log"
sleep 10
STUB
chmod 0755 /usr/local/bin/hearth-stub-browser
asuser mkdir -p "/home/$user/.local/share/applications"
printf '[Desktop Entry]\nType=Application\nName=Stub\nExec=/usr/local/bin/hearth-stub-browser %%u\nMimeType=x-scheme-handler/http;\n' |
	asuser tee "/home/$user/.local/share/applications/hearth-stub.desktop" >/dev/null
asuser env HOME="/home/$user" gio mime x-scheme-handler/http hearth-stub.desktop >/dev/null
stub_saw() { test "$(grep -c '^303$' "$stublog" 2>/dev/null)" -ge "$1"; }
portal_saw() { test "$(grep -c "^$1\$" "$portallog" 2>/dev/null)" -ge "$2"; }
stub_alive() { pgrep -f '^/bin/sh /usr/local/bin/hearth-stub-browser' >/dev/null; }
hearthds() { pgrep -u "$user" -cfx /usr/bin/hearthd; }
fake_portal() { # fake_portal response-code
	asuser hearth-fakeportal -log "$portallog" -response "$1" &
	wait_until 5 asuser gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
		--method org.freedesktop.DBus.GetNameOwner org.freedesktop.portal.Desktop || true
}
stop_portal() { pkill -f hearth-fakeportal || true; wait_until 3 not pgrep -f hearth-fakeportal || true; }

before=$(cat "$tokenfile")
check "no portal: hearthd open succeeds through GIO" opener
check "the browser reached /auth" wait_until 5 stub_saw 1
sleep 3
check "the browser is alive 3 s later" stub_alive
wait_until 5 test "$before" != "$(cat "$tokenfile")" || true
check "the token rotated after the exchange" test "$before" != "$(cat "$tokenfile")"
check "no bad token in the exchange" not grep -q 403 "$stublog"
second=$(cat "$tokenfile")

fake_portal 0
check "with a portal: hearthd open succeeds" opener
check "the portal got the link and it worked" wait_until 5 portal_saw 303 1
check "GIO was not used as well" test "$(grep -c '^303$' "$stublog")" = 1
stop_portal
wait_until 5 test "$second" != "$(cat "$tokenfile")" || true
third=$(cat "$tokenfile")

fake_portal 2
check "a failing portal: hearthd open still succeeds" opener
check "the portal refused" wait_until 5 portal_saw refused 1
check "and GIO took over" wait_until 5 stub_saw 2
stop_portal

check "the launch tokens are in no log" not grep -qF -e "$before" -e "$second" -e "$third" <<<"$(journalctl --no-pager -o verbose 2>/dev/null)"
check "no login link in any log" not grep -q 'auth?t=' <<<"$(journalctl --no-pager -o verbose 2>/dev/null)"
check "nor in any unit's description" not grep -qE "auth\?t=|$before|$second|$third" <<<"$(asuser systemctl --user list-units --all --plain --no-legend)"
check "no transient unit was made for the browser" test "$units_before" = "$(asuser systemctl --user list-units --all --plain --no-legend 'run-*' | awk '{print $1}')"

asuser systemctl --user stop hearthd.service
check "stopped" not port_open "$port"
check "hearthd open starts the service" opener
check "it is active" asuser systemctl --user is-active hearthd.service
check "and reached /auth" wait_until 5 stub_saw 3
check "one hearthd" test "$(hearthds)" = 1
check "a second run succeeds" opener
check "and reached /auth" wait_until 5 stub_saw 4
check "still one hearthd" test "$(hearthds)" = 1
pkill -f hearth-stub-browser || true

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
check "system drop-ins removed" test ! -e /etc/systemd/system/itest-limits.service.d
for f in /usr/bin/hearth-broker /usr/lib/systemd/system/hearth-broker.socket \
	/usr/lib/systemd/system/hearth-broker.service /usr/lib/systemd/system/hearth-needrestart.service \
	/usr/share/polkit-1/actions/org.hearth.policy \
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

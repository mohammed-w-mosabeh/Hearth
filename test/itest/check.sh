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
app_inspect=$(asuser /usr/bin/hearthd broker-call docker.containers.inspect '{"id":"app"}' 2>&1 || true)
check "inspect masks an app's secret by its label, whatever its name" grep -q 'ADMIN_CODE=\*\*\*\*\*\*\*\*' <<<"$app_inspect"
check "inspect keeps the app's plain values" grep -q 'GREETING=hello' <<<"$app_inspect"
check "inspect never shows the app's secret" not grep -q ITEST-APPSECRET <<<"$app_inspect"
check "nor does hearthd's container page" not grep -q ITEST-APPSECRET <<<"$(curl -s -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/docker/containers/app")"
check "hearthd's container page masks it" grep -q 'ADMIN_CODE=\*\*\*\*\*\*\*\*' <<<"$(curl -s -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/docker/containers/app")"
check "inspect reports S12 risks" grep -q '"risks":\["privileged","host-mount:/etc"\]' <<<"$inspect"
check "authorized start reaches the engine" call_says '"ok":true' docker.containers.start '{"id":"web"}'
check "engine got the start" engine_got '^POST /v1.44/containers/web/start$'
check "remove refused without polkit authorization" call_says "not authorized" docker.containers.remove '{"id":"web"}'
check "engine never got the remove" not engine_got '^DELETE'
check "no create method" call_says "unknown method" docker.containers.create '{"Image":"alpine","HostConfig":{"Privileged":true}}'
check "path injection refused" call_says "id must be" docker.containers.inspect '{"id":"../../version"}'
check "engine only got allowlisted requests" not grep -vE '^(GET /v1.44/(version|containers/json\?all=true&size=false|containers/(web|app)/json)|POST /v1.44/containers/web/start)$' /run/fakedocker.log
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
# Stack refusals give their reason, which quotes the refused name or path
# (clipped; never a value): shown here, kept out of the raw-params check.
check "audit holds params hashes, never raw params" not grep -Eq 'hearth-pentest-nx|\.\./|detachKeys|LD_PRELOAD' <<<"$(grep -v '"method":"stack\.' /var/lib/hearth/audit.jsonl)"
stack_audit=$(grep '"method":"stack\.' /var/lib/hearth/audit.jsonl || true)
check "stack audit lines carry a params hash, not the params" not grep -q '"spec"\|"secrets":{\|"version":1' <<<"$stack_audit"
echo "  stack refusals in the audit: $(grep -c '"method":"stack\.' /var/lib/hearth/audit.jsonl); quoting a probe path or name: $(grep '"method":"stack\.' /var/lib/hearth/audit.jsonl | grep -cE 'hearth-pentest|\.\./')"
# head closes the pipe early: under pipefail that SIGPIPE must not end the run.
grep '"method":"stack\.' /var/lib/hearth/audit.jsonl | grep -E 'hearth-pentest|\.\./' | head -n 3 | sed 's/^/    e.g. /' || true
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
# PackageKit is in the container (for 11.10): an unprotected package is
# previewed through its simulation, with a token.
echo "  remove curl: $(preview_remove curl | head -c 300)"
check "with PackageKit an unprotected removal is previewed" grep -q '"token"' <<<"$(preview_remove curl)"
check "extra managers listed inside the sandbox" grep -q '"manager":"pip","available"' <<<"$(pkgs_api pkgs/pip)"
check "unknown manager refused" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/pkgs/apt-get")" = 404
check "removing a flatpak that is not installed is refused" grep -q 'not installed' <<<"$(curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" \
	-d '{"target":"system/app/org.example.App/x86_64/stable","preview":true}' "http://127.0.0.1:$port/api/v1/actions/flatpak/remove")"

detail=$(pkgs_api "pkgs/apt/bash:$(dpkg --print-architecture)")
check "package detail lists bash's files" grep -q '"/usr/bin/bash"\|"/bin/bash"' <<<"$detail"
check "package detail lists installed reverse deps" grep -q '"reverse_deps":\[' <<<"$detail"
check "detail of a missing package is 404" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/pkgs/apt/nope:amd64")" = 404
echo "  updates: $(pkgs_api pkgs/updates | head -c 300)"
check "updates are listed through PackageKit" grep -q '"updates":\[' <<<"$(pkgs_api pkgs/updates)"
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

echo "== read-only tokens and the hearth command"
hearth_cli() { asuser env HOME="/home/$user" /usr/bin/hearth "$@" 2>&1 || true; }
ro_code() { curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ro" "$@"; }
check "binary hearth root-owned 0755" test "$(stat -c '%U %a' /usr/bin/hearth)" = "root 755"
check "without a token it says what to do" grep -q 'Tokens page' <<<"$(hearth_cli status)"
check "a token is created" grep -q '"name":"cli"' <<<"$(post tokens '{"name":"cli"}')"
tokfile="/home/$user/.config/hearth/tokens/cli.token"
check "its file is 0600 in a 0700 directory" test "$(stat -c '%a' "$tokfile") $(stat -c '%a' "${tokfile%/*}")" = "600 700"
ro=$(cat "$tokfile")
check "the list never carries a token" not grep -q hearth_ro_ <<<"$(pkgs_api tokens)"
check "status answers" grep -q 'Open alerts' <<<"$(hearth_cli status)"
check "ports lists hearthd's own port" grep -q "$port" <<<"$(hearth_cli ports)"
check "alerts answers" grep -q 'SEVERITY' <<<"$(hearth_cli alerts)"
check "units lists hearthd" grep -q 'hearthd.service' <<<"$(hearth_cli units)"
check "containers answers" grep -q 'IMAGE' <<<"$(hearth_cli containers)"
check "--json is JSON" jq -e .units <<<"$(hearth_cli units --json)"
check "a token cannot act" test "$(ro_code -X POST -H "Origin: http://127.0.0.1:$port" "http://127.0.0.1:$port/api/v1/actions/system/hostname")" = 403
check "a token cannot open a terminal" test "$(ro_code "http://127.0.0.1:$port/api/v1/terminal")" = 403
check "a token cannot read the journal" test "$(ro_code "http://127.0.0.1:$port/api/v1/journal")" = 403
check "a token cannot export settings" test "$(ro_code "http://127.0.0.1:$port/api/v1/backup/export")" = 403
check "/metrics is off by default" test "$(ro_code "http://127.0.0.1:$port/metrics")" = 404
curl -s -X PUT -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d '{"enabled":true}' "http://127.0.0.1:$port/api/v1/tokens/metrics"
check "/metrics speaks OpenMetrics" grep -q '^# EOF$' <<<"$(curl -s -H "Authorization: Bearer $ro" "http://127.0.0.1:$port/metrics")"
check "/metrics needs a token" test "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/metrics")" = 401
curl -s -X DELETE -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" "http://127.0.0.1:$port/api/v1/tokens/cli"
check "revoking removes the file" not test -e "$tokfile"
check "a revoked token is refused" test "$(ro_code "http://127.0.0.1:$port/api/v1/session")" = 401

echo "== compose update"
check "unit installed" test -f /usr/lib/systemd/system/hearth-compose.service
check "its unit denies every address" test "$(systemctl show -p IPAddressDeny --value hearth-compose.service)" != ""
check "it holds no capability" test -z "$(systemctl show -p CapabilityBoundingSet --value hearth-compose.service | sed 's/^0*$//')"
check "polkit asks every time" grep -A8 'org.hearth.docker.compose"' /usr/share/polkit-1/actions/org.hearth.policy

echo "== stacks (9.2)"
check "stack unit installed" test -f /usr/lib/systemd/system/hearth-stack@.service
check "it denies every address" test "$(systemctl show -p IPAddressDeny --value hearth-stack@probe.service)" != ""
check "it holds no capability" test -z "$(systemctl show -p CapabilityBoundingSet --value hearth-stack@probe.service | sed 's/^0*$//')"
check "it runs Compose on the stack's files only" grep -q -- '--file /var/lib/hearth/stacks/%i/compose.json --env-file /var/lib/hearth/stacks/%i/secrets.env $STACK_ARGS$' /usr/lib/systemd/system/hearth-stack@.service
systemctl start hearth-stack@probe.service || true
check "it does not run without a stack" test "$(systemctl show -p ConditionResult --value hearth-stack@probe.service)" = "no"
usermod -aG hearth "$user" # a member: the calls below reach the broker's checks
spec_ok='{"spec":{"version":1,"name":"probe","services":{"web":{"image":"nginx:1.27","ports":[{"host":8080,"container":80}]}}}}'
spec_lan='{"spec":{"version":1,"name":"probe","services":{"web":{"image":"nginx:1.27","ports":[{"host_ip":"0.0.0.0","host":8080,"container":80}]}}}}'
check "stack.validate accepts a plain spec" call_says '"refused": *\[\]' stack.validate "$spec_ok"
check "stack.validate names a flag" call_says '"port:0.0.0.0:8080/tcp"' stack.validate "$spec_lan"
check "stack.validate refuses privileged" call_says 'unknown key' stack.validate '{"spec":{"version":1,"name":"p","services":{"w":{"image":"nginx","privileged":true}}}}'
check "stack.validate refuses a bind of /etc" call_says 'never allowed' stack.validate '{"spec":{"version":1,"name":"p","services":{"w":{"image":"nginx","volumes":[{"type":"bind","source":"/etc","target":"/x"}]}}}}'
check "stack.validate writes nothing" not test -e /var/lib/hearth/stacks
gpasswd -d "$user" hearth >/dev/null

echo "== path checks (9.3)"
pathcheck() { # pathcheck SPEC: one request on hearth-pathcheck.socket, as root
	node -e 'const c=require("net").createConnection(process.argv[1]);let o="";c.on("data",d=>o+=d);c.on("end",()=>process.stdout.write(o));c.on("error",e=>{process.stdout.write("error "+e.code);});c.write(JSON.stringify({spec:JSON.parse(process.argv[2])})+"\n")' /run/hearth/pathcheck.sock "$1"
}
pc_says() { grep -q -- "$1" <<<"$(pathcheck "$2" 2>&1)"; }
pc_spec() { # pc_spec ROOT SOURCE
	printf '{"version":1,"name":"probe","bind_roots":["%s"],"services":{"web":{"image":"nginx:1.27","volumes":[{"type":"bind","source":"%s","target":"/data"}]}}}' "$1" "$2"
}
check "pathcheck socket listening" systemctl is-active hearth-pathcheck.socket
check "its socket is root's alone" test "$(stat -c '%U %G %a' /run/hearth/pathcheck.sock)" = "root root 600"
check "its unit holds only CAP_DAC_READ_SEARCH" test "$(systemctl show -p CapabilityBoundingSet --value hearth-pathcheck@x.service)" = "cap_dac_read_search"
check "its unit has no network" test "$(systemctl show -p PrivateNetwork --value hearth-pathcheck@x.service)" = yes
check "its unit sees home read-only" test "$(systemctl show -p ProtectHome --value hearth-pathcheck@x.service)" = read-only
mkdir -p /srv/itest-app/data "/home/$user/itest-media/films"
ln -sfn /etc /srv/itest-app/esc
ln -sfn /srv/itest-app/esc /srv/itest-app/chain
ln -sfn /proc/self/root/etc /srv/itest-app/magic
ln -sfn data /srv/itest-app/inside
check "a plain bind resolves" pc_says '"ok":true' "$(pc_spec /srv/itest-app /srv/itest-app/data)"
check "a link inside its root resolves" pc_says '"real":"/srv/itest-app/data"' "$(pc_spec /srv/itest-app /srv/itest-app/inside)"
check "a bind under home resolves" pc_says '"ok":true' "$(pc_spec "/home/$user/itest-media" "/home/$user/itest-media/films")"
check "a link to /etc is refused" pc_says 'never allowed' "$(pc_spec /srv/itest-app /srv/itest-app/esc)"
check "a chain to /etc is refused" pc_says 'never allowed' "$(pc_spec /srv/itest-app /srv/itest-app/chain)"
check "a /proc magic link is refused" pc_says '/proc link' "$(pc_spec /srv/itest-app /srv/itest-app/magic)"
check "a missing source is refused" pc_says 'does not exist' "$(pc_spec /srv/itest-app /srv/itest-app/nope)"
check "a root linked to /etc is refused" pc_says 'cannot be a root' "$(pc_spec /srv/itest-app/esc /srv/itest-app/esc/x)"
check "a non-JSON request is refused" grep -q 'not a path check request' <<<"$(node -e 'const c=require("net").createConnection("/run/hearth/pathcheck.sock");let o="";c.on("data",d=>o+=d);c.on("end",()=>process.stdout.write(o));c.write("services: {}\n")')"
check "a user cannot ask it" grep -q 'error EACCES' <<<"$(asuser node -e 'const c=require("net").createConnection("/run/hearth/pathcheck.sock");c.on("error",e=>process.stdout.write("error "+e.code));c.on("connect",()=>process.stdout.write("connected"))' 2>&1)"
check "it created nothing" test ! -e /srv/itest-app/nope
check "it wrote no stack" not test -e /var/lib/hearth/stacks
echo "== uninstall completely (11.6)"
for a in uninstall restore; do
	check "polkit has org.hearth.packages.$a" grep -q "action id=\"org.hearth.packages.$a\"" /usr/share/polkit-1/actions/org.hearth.policy
done
for u in hearth-archive@.service hearth-restore-leftovers@.service hearth-purge@.service hearth-removed-clean.service; do
	check "$u is installed" test -f /usr/lib/systemd/system/$u
done
check "the archives' clean-up timer is enabled" systemctl is-enabled hearth-removed-clean.timer
check "the clean-up runs and keeps what is fresh" systemctl start hearth-removed-clean.service
echo "== uninstall completely: acceptance (11.10)"
home=/home/$user
usermod -aG hearth "$user" # the broker serves members of hearth only
# Sample packages in a local repository: an app with two dependencies only
# it needs, one the user marked as installed by hand, and a conffile; a
# package that was autoremovable before; a package owning a folder named
# like the app; one whose removal fails; one for the swap attack.
udeb() { # udeb name depends [conffile]
	local d=/tmp/udeb/$1
	mkdir -p "$d/DEBIAN" "$d/usr/share/doc/$1"
	printf 'Package: %s\nVersion: 1.0\nArchitecture: all\nMaintainer: itest <itest@example.invalid>\n' "$1" >"$d/DEBIAN/control"
	[ -z "$2" ] || printf 'Depends: %s\n' "$2" >>"$d/DEBIAN/control"
	printf 'Description: itest uninstall sample %s\n' "$1" >>"$d/DEBIAN/control"
	echo "$1" >"$d/usr/share/doc/$1/README"
	if [ -n "${3:-}" ]; then
		mkdir -p "$d$(dirname "$3")"
		echo default >"$d$3"
		echo "$3" >"$d/DEBIAN/conffiles"
	fi
}
rm -rf /tmp/udeb /srv/itest-repo && mkdir -p /srv/itest-repo
udeb itest-un-lib1 ''
udeb itest-un-lib2 ''
udeb itest-un-keep ''
udeb itest-un-old ''
udeb itest-un-app 'itest-un-lib1, itest-un-lib2, itest-un-keep' /etc/itest-un-app/app.conf
udeb itest-un-other ''
mkdir -p /tmp/udeb/itest-un-other/var/lib/itest-un-app && echo x >/tmp/udeb/itest-un-other/var/lib/itest-un-app/data
udeb itest-un-b ''
udeb itest-un-fail ''
printf '#!/bin/sh\necho "itest: refusing to be removed" >&2\nexit 1\n' >/tmp/udeb/itest-un-fail/DEBIAN/prerm
chmod 0755 /tmp/udeb/itest-un-fail/DEBIAN/prerm
for d in /tmp/udeb/*; do dpkg-deb --build "$d" /srv/itest-repo/ >/dev/null; done
(cd /srv/itest-repo && apt-ftparchive packages . >Packages)
echo 'deb [trusted=yes] file:/srv/itest-repo ./' >/etc/apt/sources.list.d/itest-un.list
apt_local() { apt-get "$@" -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/itest-un.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0; }
apt_local update -qq >/dev/null 2>&1
apt_local install -y -qq itest-un-app itest-un-old itest-un-other itest-un-b itest-un-fail >/dev/null 2>&1
apt-mark auto itest-un-old >/dev/null && apt-mark manual itest-un-keep >/dev/null
check "samples installed" dpkg -s itest-un-app itest-un-lib1 itest-un-lib2 itest-un-keep itest-un-old
check "itest-un-old was autoremovable before" grep -q 'Remv itest-un-old' <<<"$(apt-get -s autoremove 2>/dev/null)"
echo edited >/etc/itest-un-app/app.conf
asuser mkdir -p "$home/.config/itest-un-app" "$home/.cache/itest-un-app" "$home/.config/autostart" "$home/.config/itest-un-b" "$home/.config/itest-un-fail"
echo cache | asuser tee "$home/.config/itest-un-app/settings" >/dev/null
mkdir -p /var/cache/itest-un-app && echo x >/var/cache/itest-un-app/blob
cat >/etc/polkit-1/rules.d/53-hearth-itest-uninstall.rules <<'RULES'
polkit.addRule(function(action, subject) {
	var ok = ["org.freedesktop.packagekit.package-remove", "org.freedesktop.packagekit.package-install",
		"org.freedesktop.packagekit.package-install-untrusted", "org.hearth.packages.uninstall", "org.hearth.packages.restore"];
	if (subject.user == "tester" && ok.indexOf(action.id) >= 0) {
		return polkit.Result.YES;
	}
});
RULES
sleep 1
uplan() { pkgs_api "uninstall/plan?item=$1"; }
ucode() { curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/uninstall/plan?item=$1"; }
# every key the plan offers (remains excluded)
uselect() { jq -c '[.candidates[] | select(.group != "remains") | .group + "|" + .id]' <<<"$1"; }
upreview() { # upreview item select-json
	post actions/uninstall/remove "$(jq -nc --arg t "$1" --arg s "$2" '{target:$t, params:{select:$s}, preview:true}')"
}
urun() { # urun item select-json token confirm
	post actions/uninstall/remove "$(jq -nc --arg t "$1" --arg s "$2" --arg k "$3" --arg c "$4" '{target:$t, params:{select:$s}, token:$k, confirm:$c}')"
}
uninstall_all() { # uninstall_all item -> the run's result
	local p s pv tok conf
	p=$(uplan "$1")
	s=$(uselect "$p")
	pv=$(upreview "$1" "$s")
	tok=$(jq -r '.token // empty' <<<"$pv")
	[ -n "$tok" ] || echo "  preview without a token: $(head -c 800 <<<"$pv")" >&2
	conf=$(jq -r '.confirm // empty' <<<"$pv")
	urun "$1" "$s" "$tok" "$conf"
}

p=$(uplan package:itest-un-app)
echo "  plan: $(jq -c '[.candidates[] | .group + " " + .id + " " + (.preselect|tostring) + " (" + .reason + ")"]' <<<"$p" | head -c 2500)"
in_group() { jq -e --arg g "$1" --arg id "$2" '[.candidates[] | select(.group == $g and .id == $id)] | length > 0' <<<"$p" >/dev/null; }
check "the plan offers the newly orphaned dependencies" in_group orphans itest-un-lib1:all
check "both of them" in_group orphans itest-un-lib2:all
check "a package already autoremovable is not offered" not grep -q 'itest-un-old' <<<"$p"
check "a dependency installed by hand is not offered" not grep -q 'itest-un-keep' <<<"$p"
check "its configuration is purged" in_group purge itest-un-app:all
check "a user leftover named like it is offered, unchecked" jq -e --arg id "$home/.config/itest-un-app" '.candidates[] | select(.id == $id and .group == "user" and .preselect == false)' <<<"$p"
check "a shared folder is never offered" not grep -q '"/home/tester/.config/autostart"' <<<"$p"
check "an unowned system leftover is offered, unchecked" jq -e '.candidates[] | select(.id == "/var/cache/itest-un-app" and .group == "system" and .preselect == false)' <<<"$p"
check "a folder another package owns stays" in_group remains /var/lib/itest-un-app
check "an Essential package is blocked" grep -q 'essential' <<<"$(uplan package:bash | jq -r .blocked)"
check "Hearth itself is blocked" grep -q 'self' <<<"$(uplan package:hearth | jq -r .blocked)"
for bad in 'package:../x' 'package:-x' 'package:a%2Fb' '../etc' 'shell:rm' 'app:..%2F..%2Fetc'; do
	check "a crafted item is refused: $bad" test "$(ucode "$bad")" -ge 400
done
res=$(uninstall_all package:itest-un-app)
echo "  run: $res"
check "the uninstall ran without a failure" jq -e '.failed == [] and .done > 0' <<<"$res"
check "the app is purged" not dpkg-query -W itest-un-app
check "its orphaned dependencies are gone" not dpkg-query -W itest-un-lib1 itest-un-lib2
check "the autoremovable-before package stays" dpkg -s itest-un-old
check "the hand-installed dependency stays" dpkg -s itest-un-keep
check "its configuration folder is gone" test ! -e /etc/itest-un-app
check "the user leftover is in the trash" grep -rqs "itest-un-app" "$home/.local/share/Trash/info"
check "and not where it was" test ! -e "$home/.config/itest-un-app"
check "the system leftover is gone" test ! -e /var/cache/itest-un-app
check "and archived with a manifest" grep -qs '/var/cache/itest-un-app' /var/lib/hearth/removed/*/manifest.json
check "the conffile was copied into the archive first" grep -qs '/etc/itest-un-app/app.conf' /var/lib/hearth/removed/*/manifest.json
check "the archives are root's only" test "$(stat -c %a /var/lib/hearth/removed)" = 700
check "every step is audited" grep -q 'uninstall.program' <<<"$(sqlite3 "$home/.local/share/hearth/hearth.db" "SELECT action FROM audit_log WHERE action LIKE 'uninstall.%'")"
ops=$(pkgs_api uninstall/ops)
check "Recently removed lists it" jq -e '.[] | select(.item == "package:itest-un-app" and .state == "done")' <<<"$ops"

echo "  -- a leftover swapped for a link to /etc between preview and run"
pb=$(uplan package:itest-un-b)
sb=$(uselect "$pb")
pvb=$(upreview package:itest-un-b "$sb")
asuser rm -rf "$home/.config/itest-un-b" && asuser ln -s /etc "$home/.config/itest-un-b"
resb=$(urun package:itest-un-b "$sb" "$(jq -r .token <<<"$pvb")" "$(jq -r '.confirm // empty' <<<"$pvb")")
echo "  run: $resb"
check "the swapped path is refused" grep -q 'changed since the preview' <<<"$resb"
check "nothing ran: the package is still installed" dpkg -s itest-un-b
check "the link is still there, and /etc with it" test -L "$home/.config/itest-un-b" -a -f /etc/passwd

echo "  -- a failure mid-way"
resf=$(uninstall_all package:itest-un-fail)
echo "  run: $resf"
check "the run reports the failure" jq -e '.failed | length > 0' <<<"$resf"
check "and stops: the leftover after it is untouched" test -d "$home/.config/itest-un-fail"
check "the record says where it stopped" jq -e '.[] | select(.item == "package:itest-un-fail" and .state == "failed" and .failed == "program")' <<<"$(pkgs_api uninstall/ops)"

echo "  -- restore"
opid=$(jq -r '.[] | select(.item == "package:itest-un-app") | .id' <<<"$ops")
rpv=$(post actions/uninstall/restore "$(jq -nc --arg t "$opid" '{target:$t, preview:true}')")
echo "  restore preview: $(head -c 600 <<<"$rpv")"
rres=$(post actions/uninstall/restore "$(jq -nc --arg t "$opid" --arg k "$(jq -r .token <<<"$rpv")" '{target:$t, token:$k}')")
echo "  restore: $rres"
check "the restore ran without a failure" jq -e '.failed == []' <<<"$rres"
journalctl -q --no-pager -n 15 -u 'hearth-restore-leftovers@*' | sed 's/^/  | /' || true
check "the package is back" dpkg -s itest-un-app
check "with the user's configuration, not the default" grep -qx edited /etc/itest-un-app/app.conf
check "the user leftover is back from the trash" test -f "$home/.config/itest-un-app/settings"
check "the system leftover is back from the archive" test -f /var/cache/itest-un-app/blob
check "the operation is marked restored" jq -e '.[] | select(.id == "'"$opid"'" and .state == "restored")' <<<"$(pkgs_api uninstall/ops)"

echo "  -- an AppImage and its desktop integration"
asuser mkdir -p "$home/Applications" "$home/.local/share/applications" "$home/.local/share/icons/hicolor/48x48/apps"
printf '#!/bin/true\n' | asuser tee "$home/Applications/ItestApp.AppImage" >/dev/null && chmod 0755 "$home/Applications/ItestApp.AppImage"
printf '[Desktop Entry]\nType=Application\nName=Itest App\nExec=%s %%U\nIcon=appimagekit_abc_ItestApp\n' "$home/Applications/ItestApp.AppImage" | asuser tee "$home/.local/share/applications/appimagekit_abc-ItestApp.desktop" >/dev/null
printf 'png' | asuser tee "$home/.local/share/icons/hicolor/48x48/apps/appimagekit_abc_ItestApp.png" >/dev/null
appimage_listed() { test "$(ucode app:appimage.itestapp)" = 200; }
check "the AppImage is listed (a new folder is watched within a minute)" wait_until 90 appimage_listed
resa=$(uninstall_all app:appimage.itestapp)
echo "  run: $resa"
check "the AppImage uninstall ran" jq -e '.failed == [] and .done > 0' <<<"$resa"
check "the AppImage is gone" test ! -e "$home/Applications/ItestApp.AppImage"
check "its desktop entry is gone" test ! -e "$home/.local/share/applications/appimagekit_abc-ItestApp.desktop"
check "its icon is gone" test ! -e "$home/.local/share/icons/hicolor/48x48/apps/appimagekit_abc_ItestApp.png"
check "all three are in the trash" test "$(grep -rls 'ItestApp' "$home/.local/share/Trash/info" | wc -l)" -ge 3

echo "  -- a Flatpak (a stub flatpak: a real one needs a runtime and the network)"
fp=$home/.local/share/flatpak
asuser mkdir -p "$fp/app/org.itest.App/x86_64/stable/active/files" "$fp/exports/share/applications" "$home/.var/app/org.itest.App/config"
printf '[Application]\nname=org.itest.App\n' | asuser tee "$fp/app/org.itest.App/x86_64/stable/active/metadata" >/dev/null
printf '[Desktop Entry]\nType=Application\nName=Itest Flatpak\nExec=/usr/bin/flatpak run org.itest.App\nX-Flatpak=org.itest.App\n' | asuser tee "$fp/exports/share/applications/org.itest.App.desktop" >/dev/null
echo data | asuser tee "$home/.var/app/org.itest.App/config/state" >/dev/null
cat >/usr/bin/flatpak <<'STUB'
#!/bin/sh
# itest stub: only what Hearth's uninstall runs, "flatpak uninstall -y --user <ref>".
[ "$1" = uninstall ] && [ "$2" = -y ] && [ "$3" = --user ] || exit 2
id=$(echo "$4" | cut -d/ -f2)
rm -rf "$HOME/.local/share/flatpak/app/$id" "$HOME/.local/share/flatpak/exports/share/applications/$id.desktop"
echo "$*" >>"$HOME/.flatpak-stub.log"
STUB
chmod 0755 /usr/bin/flatpak
resp=$(uninstall_all flatpak:user/app/org.itest.App/x86_64/stable)
echo "  run: $resp"
check "the Flatpak uninstall ran" jq -e '.failed == [] and .done > 0' <<<"$resp"
check "flatpak uninstall was asked for that ref only" grep -qx 'uninstall -y --user app/org.itest.App/x86_64/stable' "$home/.flatpak-stub.log"
check "its app data is in the trash" test ! -e "$home/.var/app/org.itest.App" -a -n "$(grep -rls 'org.itest.App' "$home/.local/share/Trash/info")"
rm -f /usr/bin/flatpak "$home/.flatpak-stub.log"
apt_local remove -y -qq itest-un-app itest-un-old itest-un-other itest-un-b itest-un-fail itest-un-keep >/dev/null 2>&1 || true
rm -f /etc/apt/sources.list.d/itest-un.list /etc/polkit-1/rules.d/53-hearth-itest-uninstall.rules

echo "== stack lifecycle (9.4)"
for a in deploy control remove danger grant; do
	check "polkit has org.hearth.stack.$a" grep -q "action id=\"org.hearth.stack.$a\"" /usr/share/polkit-1/actions/org.hearth.policy
done
usermod -aG hearth "$user" # a member without a polkit agent
stack_ok='{"version":1,"name":"probe","services":{"web":{"image":"nginx:1.27"}}}'
deploy() { printf '{"spec":%s,"ack_flags":%s}' "$1" "${2:-[]}"; }
check "stack.status lists no stack" call_says '\[\]' stack.status
check "deploy: privileged refused before polkit" call_says 'unknown key' stack.deploy "$(deploy '{"version":1,"name":"probe","services":{"web":{"image":"nginx","privileged":true}}}')"
check "deploy: an unacknowledged flag is refused" call_says 'not the spec' stack.deploy "$(deploy '{"version":1,"name":"probe","opt_in":["cap:NET_ADMIN"],"services":{"web":{"image":"nginx","cap_add":["NET_ADMIN"]}}}')"
polkit_logged() { grep -q -- "$1" <<<"$(journalctl -t hearth-itest --no-pager 2>/dev/null)"; }
# A rule that records what polkit was asked, then refuses: the danger
# action, with the flags in its details for the dialog. polkitd runs at
# --log-level=notice, which hides polkit.log, so it spawns logger.
cat >/etc/polkit-1/rules.d/53-hearth-itest-danger.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.stack.danger" || action.id == "org.hearth.stack.deploy") {
		polkit.spawn(["/usr/bin/logger", "-t", "hearth-itest", action.id + " app=" + action.lookup("app") + " flags=" + action.lookup("flags")]);
		return polkit.Result.NO;
	}
});
RULES
sleep 1 # polkitd reloads rules on change
check "deploy: an acknowledged flag reaches polkit" call_says 'not authorized' stack.deploy "$(deploy '{"version":1,"name":"probe","opt_in":["cap:NET_ADMIN"],"services":{"web":{"image":"nginx","cap_add":["NET_ADMIN"]}}}' '["cap:NET_ADMIN"]')"
check "it asks org.hearth.stack.danger, naming the flags" wait_until 5 polkit_logged 'org.hearth.stack.danger app=probe flags=cap:NET_ADMIN'
check "deploy: a bind of /etc is refused" call_says 'never allowed' stack.deploy "$(deploy '{"version":1,"name":"probe","services":{"web":{"image":"nginx","volumes":[{"type":"bind","source":"/etc","target":"/x"}]}}}')"
check "deploy: a link to /etc is refused by the path check" call_says 'resolves to /etc' stack.deploy "$(deploy "$(pc_spec /srv/itest-app /srv/itest-app/esc)")"
check "deploy: a missing bind is refused" call_says 'does not exist' stack.deploy "$(deploy "$(pc_spec /srv/itest-app /srv/itest-app/nope)")"
check "deploy: YAML is refused" call_says 'not an object' stack.deploy '{"spec":"services: {}","ack_flags":[]}'
check "deploy: a clean spec reaches polkit" call_says 'not authorized' stack.deploy "$(deploy "$stack_ok")"
check "a plain deploy asks org.hearth.stack.deploy" wait_until 5 polkit_logged 'org.hearth.stack.deploy app=undefined'
check "deploy: a bind that resolves reaches polkit" call_says 'not authorized' stack.deploy "$(deploy "$(pc_spec /srv/itest-app /srv/itest-app/inside)")"
check "audit records the refusal" grep -q '"method":"stack.deploy".*refused: not authorized' /var/lib/hearth/audit.jsonl
check "control of a missing stack is refused" call_says 'no such stack' stack.control '{"name":"probe","verb":"start"}'
check "control verbs are closed" call_says 'verb must be' stack.control '{"name":"probe","verb":"down-volumes"}'
check "remove of a missing stack is refused" call_says 'no such stack' stack.remove '{"name":"probe"}'
check "update of a missing stack is refused" call_says 'no such stack' stack.update "{\"name\":\"probe\",\"spec_sha256\":\"$(printf '0%.0s' {1..64})\",\"pins\":{\"web\":\"sha256:$(printf 'a%.0s' {1..64})\"}}"
check "nothing was written" not test -e /var/lib/hearth/stacks
check "a read-only token cannot ask for it" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${ro_key:-none}" "http://127.0.0.1:$port/api/v1/stacks/import/nosuch")" != 200
rm -f /etc/polkit-1/rules.d/53-hearth-itest-danger.rules
echo "== app names (9.13)"
key=$(api_key)
proxy_act() {
	curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' \
		-d "$2" "http://127.0.0.1:$port/api/v1/actions/proxy/$1"
}
proxy_get() { curl -s -o /dev/null -w '%{http_code}' -H "Host: $1" "http://127.0.0.1:8470/"; }
st=$(curl -s -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/proxy")
check "app names are off by default" grep -q '"enabled":false' <<<"$st"
check "and nothing listens for them" not port_open 8470
check "Hearth's own port is refused" grep -q "Hearth's own" <<<"$(proxy_act listen "{\"target\":\"proxy\",\"params\":{\"enabled\":\"true\",\"port\":\"$port\"}}")"
check "a privileged port is refused" grep -q '1024' <<<"$(proxy_act listen '{"target":"proxy","params":{"enabled":"true","port":"80"}}')"
check "no address can be chosen" grep -q 'unknown param' <<<"$(proxy_act listen '{"target":"proxy","params":{"enabled":"true","address":"0.0.0.0"}}')"
check "turning names on" grep -q '"done":1' <<<"$(proxy_act listen '{"target":"proxy","params":{"enabled":"true","port":"8470"}}')"
check "they listen" wait_until 5 port_open 8470
check "on 127.0.0.1 only" loopback_only "$(ss -tlnH 'sport = :8470' | awk '{print $4}')"
check "nothing on 0.0.0.0 / [::] for names" not wildcard_listener 8470
check "Hearth is not proxied by its host" test "$(proxy_get "localhost:8470")" = 404
check "nor by 127.0.0.1" test "$(proxy_get "127.0.0.1:8470")" = 404
check "an app with no name is not proxied" test "$(proxy_get "nope.localhost:8470")" = 502
check "a route to a port no app publishes is refused" not grep -q '"done":1' <<<"$(proxy_act route "{\"target\":\"probe\",\"params\":{\"port\":\"$port\"}}")"
check "Hearth refuses a name's Host" test "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: probe.localhost:'"$port" "http://127.0.0.1:$port/api/v1/health")" = 403
check "turning names off" grep -q '"done":1' <<<"$(proxy_act listen '{"target":"proxy","params":{"enabled":"false"}}')"
check "the port closes" wait_until 5 not port_open 8470

echo "== stack backups (9.10)"
check "polkit has org.hearth.stack.backup" grep -q 'action id="org.hearth.stack.backup"' "$policy"
check "restore asks every time (auth_admin, no keep)" grep -Pzq '(?s)org\.hearth\.stack\.restore">.*?<allow_active>auth_admin</allow_active>' "$policy"
for u in backup restore; do
	check "$u unit installed" test -f "/usr/lib/systemd/system/hearth-$u@.service"
	check "$u unit has no network" test "$(systemctl show -p PrivateNetwork --value "hearth-$u@probe.service")" = yes
	systemctl start "hearth-$u@probe.service" 2>/dev/null || true
	check "$u unit does not run without the broker's plan" test "$(systemctl show -p ConditionResult --value "hearth-$u@probe.service")" = "no"
done
check "backup unit holds only CAP_DAC_READ_SEARCH" test "$(systemctl show -p CapabilityBoundingSet --value hearth-backup@probe.service)" = "cap_dac_read_search"
check "backup unit sees home read-only" test "$(systemctl show -p ProtectHome --value hearth-backup@probe.service)" = read-only
check "restore unit cannot see home" test "$(systemctl show -p ProtectHome --value hearth-restore@probe.service)" = yes
check "restore unit cannot mount or load modules" not grep -Eq 'cap_sys_admin|cap_sys_module' <<<"$(systemctl show -p CapabilityBoundingSet --value hearth-restore@probe.service)"
check "backup of a missing stack is refused" call_says 'no such stack' stack.backup '{"name":"probe"}'
check "backup keeps 1 to 30" call_says 'keep is 1 to 30' stack.backup '{"name":"probe","keep":31}'
check "a restore without the typed name is refused" call_says 'type the app' stack.restore '{"name":"probe","archive":"20261106T000000Z.tar.zst","token":"x","typed_name":"","ack_flags":[]}'
check "a restore without a preview is refused" call_says 'preview it again' stack.restore '{"name":"probe","archive":"20261106T000000Z.tar.zst","token":"x","typed_name":"probe","ack_flags":[]}'
check "a path as an archive name is refused" call_says 'not an archive name' stack.restore.preview '{"name":"probe","archive":"../x/20261106T000000Z.tar.zst"}'
check "a listing of no backups is empty" call_says '"archives":\[\]' stack.backup.list '{"name":"probe"}'
check "no backup folder was made" not test -e /var/lib/hearth/backups
echo "== scheduled backups (9.11)"
check "scheduled unit installed" test -f /usr/lib/systemd/system/hearth-backup-scheduled@.service
check "it holds no capability" test -z "$(systemctl show -p CapabilityBoundingSet --value hearth-backup-scheduled@probe.service | sed 's/^0*$//')"
check "it cannot see home" test "$(systemctl show -p ProtectHome --value hearth-backup-scheduled@probe.service)" = yes
systemctl start hearth-backup-scheduled@probe.service 2>/dev/null || true
check "it does not run without a saved schedule" test "$(systemctl show -p ConditionResult --value hearth-backup-scheduled@probe.service)" = "no"
check "a schedule for a missing stack is refused" call_says 'no such stack' stack.backup.schedule '{"name":"probe","on_calendar":"daily"}'
check "a calendar holding a unit line is refused" call_says 'not a calendar expression' stack.backup.schedule '{"name":"probe","on_calendar":"daily\nExecStart=/bin/sh"}'
check "no timer was written" not test -e /etc/systemd/system/hearth-backup-probe.timer
echo "== stack import (9.8)"
imark=ITEST-IMPORT-4d2a
ispec='{"version":1,"name":"probe","services":{"web":{"image":"nginx:1.27","secrets":["PW","DB_PASSWORD=WEB_DB_PASSWORD"]}}}'
import_params() { printf '{"spec":%s,"ack_flags":[],"secrets":%s}' "$ispec" "$1"; }
check "import: a secret without its value is refused" call_says 'WEB_DB_PASSWORD has no value' stack.import "$(import_params "{\"PW\":\"$imark\"}")"
check "import: a secret no service names is refused" call_says 'names the secret OTHER' stack.import "$(import_params "{\"PW\":\"$imark\",\"WEB_DB_PASSWORD\":\"$imark\",\"OTHER\":\"$imark\"}")"
check "import: a value of two lines is refused" call_says 'one line' stack.import "$(import_params "{\"PW\":\"$imark\\nX=1\",\"WEB_DB_PASSWORD\":\"$imark\"}")"
check "import: a complete one reaches polkit" call_says 'not authorized' stack.import "$(import_params "{\"PW\":\"$imark\",\"WEB_DB_PASSWORD\":\"$imark\"}")"
check "import: the broker audit holds no value" not grep -q "$imark" /var/lib/hearth/audit.jsonl
check "import: the journal holds no value" not grep -q "$imark" <<<"$(journalctl --no-pager 2>/dev/null)"
check "import: nothing was written" not test -e /var/lib/hearth/stacks
echo "== stack secrets (9.5)"
check "polkit has org.hearth.stack.secrets" grep -q 'action id="org.hearth.stack.secrets"' /usr/share/polkit-1/actions/org.hearth.policy
mark=ITEST-SECRET-7c1e
check "a secret for a missing stack is refused" call_says 'no such stack' stack.secrets.set "{\"name\":\"probe\",\"set\":{\"PW\":\"$mark\"}}"
check "a reveal for a missing stack is refused" call_says 'no such stack' stack.secrets.reveal '{"name":"probe","key":"PW"}'
call_says x stack.secrets.set "{\"name\":\"probe\",\"set\":{\"PW\":\"$mark\"}} junk" || true
check "the broker audit holds no secret" not grep -q "$mark" /var/lib/hearth/audit.jsonl
check "the journal holds no secret" not grep -q "$mark" <<<"$(journalctl --no-pager 2>/dev/null)"
check "nor the app container's secret read through inspect" not grep -q ITEST-APPSECRET <<<"$(journalctl --no-pager 2>/dev/null)"
check "nor the broker audit" not grep -q ITEST-APPSECRET /var/lib/hearth/audit.jsonl
check "nor hearthd's database" not grep -rqa ITEST-APPSECRET "/home/$user/.local/share/hearth"
check "nothing was written" not test -e /var/lib/hearth/stacks
gpasswd -d "$user" hearth >/dev/null
rm -rf /srv/itest-app "/home/$user/itest-media"

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

echo "== e2e: XSS and accessibility (Playwright, Debian's chromium)"
# Hostile strings wherever the UI shows system-derived text: a container
# (name, image, status, compose project), a package description, a
# listening process's name, and a tile, a job and an alert rule.
usermod -aG hearth "$user"
touch /run/fakedocker.hostile
systemd-run --quiet --unit=fakedocker /root/fakedocker
wait_until 10 test -S /run/docker.sock || fail "fake docker started again"
# 9.7: Docker is back and the user is a member here. The socket file may
# be the stopped fake's, so retry until the new one answers.
dry_code() { curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/stacks/import/nosuch"; }
dry_is_404() { test "$(dry_code)" = 404; }
check "the import dry-run of a project without containers is 404" wait_until 10 dry_is_404
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
# A crash whose process name is the payload (Journal page), logged the way
# systemd-coredump does, into the user's own journal.
printf 'MESSAGE_ID=fc2e22bc6ee647b6b90729ab34a250b1\nMESSAGE=itest crash\nCOREDUMP_COMM=%s\nCOREDUMP_EXE=/tmp/%s\nCOREDUMP_SIGNAL_NAME=SIGSEGV\n' "$xss" "$xss" | asuser logger --journald
check "the crash report lists it" grep -q 'SIGSEGV' <<<"$(pkgs_api crashes)"
# Something holding sleep back, named with the payload (Settings page).
systemd-run --quiet --unit=itest-inhibit /usr/bin/systemd-inhibit --what=sleep --who="$xss" --why="$xss" /bin/sleep 600
sleep 1
check "the inhibitor is listed" grep -q 'onerror' <<<"$(pkgs_api power/sleep)"
# A PCI device named with the payload, through the id file (Hardware page).
pcidev=$(ls /sys/bus/pci/devices | head -1)
if [ -n "$pcidev" ]; then
	v=$(sed 's/^0x//' "/sys/bus/pci/devices/$pcidev/vendor")
	d=$(sed 's/^0x//' "/sys/bus/pci/devices/$pcidev/device")
	mkdir -p /usr/share/misc
	printf '%s  %s\n\t%s  %s\n' "$v" "$xss" "$d" "$xss" >/usr/share/misc/pci.ids
fi
check "a PCI device is listed" test -n "$pcidev"
check "no serial field in the inventory" not grep -qiE '"[a-z_]*(serial|uuid)[a-z_]*":' <<<"$(pkgs_api hardware)"
# Real Lynis in its sandbox: a run, its report, then a finding with the payload.
check "lynis.run asks polkit" call_says "not authorized" lynis.run
cat >/etc/polkit-1/rules.d/52-hearth-itest-lynis.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.lynis.run" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1
check "lynis.run takes no params" call_says "takes no params" lynis.run '{"args":"--upload"}'
check "lynis.schedule takes only the table" call_says "must be off, weekly or monthly" lynis.schedule '{"every":"hourly"}'
ly=$(asuser timeout 900 /usr/bin/hearthd broker-call lynis.run 2>&1 || true)
echo "  lynis: ${ly:0:200}"
check "real Lynis ran in its sandbox" grep -q '"index":[0-9]' <<<"$ly"
check "the report left only figures and findings" not grep -qE "$(hostname)|\"(hostname|real_user)" <<<"$ly"
check "its unit denies every address" test "$(systemctl show -p IPAddressDeny --value hearth-lynis.service)" != ""
check "its report is root's" test "$(stat -c '%U %a' /var/lib/hearth/lynis-report.dat)" = "root 600"
check "the temporary report is gone" not test -e /var/lib/hearth/lynis-report.tmp
check "the broker gained no capability for it" test "$(systemctl show -p CapabilityBoundingSet --value hearth-broker.service)" = "cap_sys_rawio cap_sys_admin"
check "lynis.report starts nothing" test "$(systemctl show -p ActiveState --value hearth-lynis.service)" = inactive
check "a schedule writes the timer" call_says '"schedule":"weekly"' lynis.schedule '{"every":"weekly"}'
check "the timer is active" systemctl is-active hearth-lynis.timer
check "the timer is enabled" systemctl is-enabled hearth-lynis.timer
check "the page reads the schedule" call_says '"schedule":"weekly"' lynis.report
check "off removes it" call_says '"schedule":"off"' lynis.schedule '{"every":"off"}'
check "the timer is gone" not test -e /etc/systemd/system/hearth-lynis.timer
check "a schedule left for purge to remove" call_says '"schedule":"monthly"' lynis.schedule '{"every":"monthly"}'
rm -f /etc/polkit-1/rules.d/52-hearth-itest-lynis.rules
sed -i "s|^hardening_index=.*|warning[]=AUTH-9999\|$xss\|$xss\|$xss\|\n&|" /var/lib/hearth/lynis-report.dat
check "a hostile finding is listed" grep -q 'onerror' <<<"$(pkgs_api security/lynis)"
# Real ufw in its sandbox, then a rule the page can only read, with the payload.
check "the firewall route answers" grep -q '"installed":true' <<<"$(pkgs_api firewall)"
check "ufw.rule asks polkit" call_says "not authorized" ufw.rule '{"op":"add","action":"allow","proto":"tcp","port":8443}'
check "ufw.set asks polkit" call_says "not authorized" ufw.set '{"enabled":true}'
risky='{"project":"app","dir":"/home/x","files":["/home/x/c.yaml"],"config":{"name":"app","services":{"web":{"image":"nginx","provider":{"type":"x"}}}}}'
clean='{"project":"app","dir":"/home/x","files":["/home/x/c.yaml"],"config":{"name":"app","services":{"web":{"image":"nginx"}}}}'
check "compose.update refuses a provider service before polkit" call_says 'refused: web: provider' compose.update "$risky"
check "compose.update refuses an unknown param" call_says 'bad params' compose.update '{"project":"app","exec":"sh"}'
check "compose.update asks polkit for a clean config" call_says "not authorized" compose.update "$clean"
check "ufw never keeps a password" grep -Pzq '(?s)org\.hearth\.firewall\.manage">.*?<allow_active>auth_admin</allow_active>' "$policy"
cat >/etc/polkit-1/rules.d/53-hearth-itest-ufw.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.hearth.firewall.manage" && subject.user == "tester") {
		return polkit.Result.YES;
	}
});
RULES
sleep 1
for bad in '{"op":"add","action":"limit","proto":"tcp","port":22}' \
	'{"op":"add","action":"allow","proto":"tcp","port":22,"interface":"eth0"}' \
	'{"op":"add","action":"allow","proto":"tcp","port":22,"source":"any to any"}' \
	'{"op":"add","action":"allow","proto":"tcp port 80","port":22}' \
	'{"op":"reset","action":"allow","proto":"tcp","port":22}'; do
	check "ufw.rule refuses $bad" call_says "bad params\|must be" ufw.rule "$bad"
done
check "a rule needs an active firewall" call_says "not active" ufw.rule '{"op":"add","action":"allow","proto":"tcp","port":8443}'
check "ufw enables in its sandbox" call_says '"enabled":true' ufw.set '{"enabled":true}'
echo "  ufw journal: $(journalctl -u hearth-ufw.service -n 3 --no-pager -o cat 2>/dev/null | tail -2 | cut -c1-160)"
check "ufw is on" grep -q '^ENABLED=yes' /etc/ufw/ufw.conf
check "and its chains are loaded" iptables -S ufw-user-input
check "ufw adds a rule" call_says '"done":true' ufw.rule '{"op":"add","action":"allow","proto":"tcp","port":8443,"source":"10.0.0.0/8"}'
check "the kernel has it" grep -q 'dport 8443' <<<"$(iptables -S ufw-user-input)"
check "and the page reads it" grep -q '"port":8443,"source":"10.0.0.0/8"' <<<"$(pkgs_api firewall)"
check "ufw deletes it" call_says '"done":true' ufw.rule '{"op":"delete","action":"allow","proto":"tcp","port":8443,"source":"10.0.0.0/8"}'
check "it left the kernel" not grep -q 'dport 8443' <<<"$(iptables -S ufw-user-input)"
check "the args file is gone" not test -e /var/lib/hearth/ufw.args
check "the broker gained no capability for ufw" test "$(systemctl show -p CapabilityBoundingSet --value hearth-broker.service)" = "cap_sys_rawio cap_sys_admin"
check "ufw disables" call_says '"enabled":false' ufw.set '{"enabled":false}'
check "ufw is off" grep -q '^ENABLED=no' /etc/ufw/ufw.conf
# A tuple's fields hold no spaces, so this payload has none.
sed -i 's|^### END RULES ###|### tuple ### limit tcp 2222 0.0.0.0/0 any 0.0.0.0/0 <img/src=x/onerror=window.__xss=1> - in\n&|' /etc/ufw/user.rules
rm -f /etc/polkit-1/rules.d/53-hearth-itest-ufw.rules
check "a hostile rule is listed read-only" grep -q 'onerror' <<<"$(pkgs_api firewall)"
# Integrity watch, as the user. The daemon may have taken its baseline already,
# so everything found now is accepted first.
integrity_post() { curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d "$1" "http://127.0.0.1:$port/api/v1/security/integrity/accept"; }
hearth_db=/home/$user/.local/share/hearth/hearth.db
asuser mkdir -p "/home/$user/.ssh" "/home/$user/.local/bin"
printf 'alias a=b\nexport API_TOKEN=hunter2\n' | asuser tee -a "/home/$user/.bashrc" >/dev/null
iv=$(pkgs_api security/integrity)
if [ "$(jq '.changes|length' <<<"$iv")" != 0 ]; then
	integrity_post "$(jq -c '{items:[.changes[]|{path,hash}]}' <<<"$iv")" >/dev/null
fi
check "a baseline exists and is clean" grep -q '"changes":\[\]' <<<"$(pkgs_api security/integrity)"
check "integrity reports sudoers.d" grep -q '"dir":"/etc/sudoers.d"' <<<"$(pkgs_api security/integrity)"
sleep 3 # past the window in which a same-size rewrite is not trusted
echo 'ssh-ed25519 AAAAitestkey me@laptop' | asuser tee -a "/home/$user/.ssh/authorized_keys" >/dev/null
printf 'alias c=d\nexport API_TOKEN=hunter3\n' | asuser tee -a "/home/$user/.bashrc" >/dev/null
iv=$(pkgs_api security/integrity)
check "a new key is seen" test "$(jq -r '.changes[]|select(.path=="~/.ssh/authorized_keys").kind' <<<"$iv")" = added
check "a changed rc file is seen" test "$(jq -r '.changes[]|select(.path=="~/.bashrc").kind' <<<"$iv")" = changed
idiff=$(pkgs_api 'security/integrity/diff?path=~/.bashrc')
check "the diff shows the change" grep -q 'alias c=d' <<<"$idiff"
check "and hides the secret" not grep -q 'hunter' <<<"$idiff"
ibase=$(sqlite3 "$hearth_db" "SELECT text FROM integrity_baseline WHERE path='~/.bashrc'")
check "the baseline has the file's text" grep -q 'alias a=b' <<<"$ibase"
check "and no secret" not grep -q 'hunter' <<<"$ibase"
check "the diff reads only watched paths" test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/security/integrity/diff?path=/etc/passwd")" = 404
check "an accept without Origin is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -d '{"items":[{"path":"~/.bashrc","hash":"x"}]}' "http://127.0.0.1:$port/api/v1/security/integrity/accept")" = 403
ia=$(integrity_post "$(jq -c '{items:[.changes[]|select(.path=="~/.bashrc")|{path,hash}]}' <<<"$iv")")
check "accept takes the shown change" test "$(jq '[.changes[]|select(.path=="~/.bashrc")]|length' <<<"$ia")" = 0
check "and leaves the others" test "$(jq '[.changes[]|select(.path=="~/.ssh/authorized_keys")]|length' <<<"$ia")" = 1
check "accepting is in the audit log" grep -q 'security.integrity.accept' <<<"$(sqlite3 "$hearth_db" 'SELECT action FROM audit_log')"
check "the alert fired" grep -q 'integrity.changed' <<<"$(pkgs_api alerts/events)"
asuser touch "/home/$user/.local/bin/$xss"
check "a hostile file name is listed" grep -q 'onerror' <<<"$(pkgs_api security/integrity)"
# Access, read-only: this user cannot read the system journal here.
ac=$(pkgs_api security/access)
check "access says the journal is out of reach" test "$(jq -r .limited <<<"$ac")" = true
check "access lists the user" test "$(jq --arg u "$user" '[.users[]|select(.name==$u)]|length' <<<"$ac")" = 1
check "access lists root first" test "$(jq -r '.users[0].name' <<<"$ac")" = root
check "access asked logind" test "$(jq -r '.sessionsError // false' <<<"$ac")" = false
check "the access command is allowed" journalctl --quiet --no-pager --output=json --reverse --lines=5000 --output-fields=MESSAGE --since=@1 _UID=0 SYSLOG_IDENTIFIER=sshd SYSLOG_IDENTIFIER=sshd-session
# A group member named with the payload (a directory service could send one).
sed -i "s|^sudo:.*|&,$xss|" /etc/group
check "a hostile member is flagged" test "$(jq '[.users[]|select(.uid==-1 and .sudo)]|length' <<<"$(pkgs_api security/access)")" = 1
# A cache folder named with the payload (Space advisor).
asuser mkdir -p "/home/$user/.cache/$xss"
# A Steam game whose name is the payload (VDF escapes its quotes).
steamapps=/home/$user/.local/share/Steam/steamapps
asuser mkdir -p "$steamapps"
printf '"AppState"\n{\n\t"appid"\t"9"\n\t"name"\t"%s"\n\t"StateFlags"\t"4"\n}\n' "${xss//\"/\\\"}" | asuser tee "$steamapps/appmanifest_9.acf" >/dev/null
# The list is a scan kept for a while, so it shows up within that time.
game_listed() { grep -q '"id":"steam.9"' <<<"$(pkgs_api games)"; }
wait_until 90 game_listed || true
check "hostile game listed" game_listed
# A managed app (Gate 9), written as the broker would, with the payload in
# every free-text place a spec allows: a bind folder, a plain value, a label,
# a command and a healthcheck. The fake Engine cannot deploy, so the app's
# row and drawer would otherwise never render on the real hearthd.
xq=${xss//\"/\\\"}
install -d -m 700 /var/lib/hearth/stacks /var/lib/hearth/stacks/xssapp
printf '{"version":1,"name":"xssapp","bind_roots":["/srv"],"services":{"web":{"image":"nginx:1.27","environment":{"NOTE":"%s"},"labels":{"note":"%s"},"command":["echo","%s"],"healthcheck":{"test":["CMD-SHELL","%s"]},"volumes":[{"type":"bind","source":"/srv/%s","target":"/data"}]}}}' "$xq" "$xq" "$xq" "$xq" "$xq" >/var/lib/hearth/stacks/xssapp/spec.json
chmod 600 /var/lib/hearth/stacks/xssapp/spec.json
check "hostile managed app listed" grep -q '"name":"xssapp"' <<<"$(curl -s -H "Authorization: Bearer $key" "http://127.0.0.1:$port/api/v1/stacks")"
e2e=$(cd /opt/e2e && HEARTH_VERSION="$HEARTH_VERSION" HEARTH_URL="http://127.0.0.1:$port/auth?t=$(cat /home/$user/.config/hearth/token)" timeout 900 npx playwright test 2>&1) || true
if grep -qE '[0-9]+ passed' <<<"$e2e" && ! grep -qE '[0-9]+ (failed|flaky)' <<<"$e2e"; then
	echo "$e2e" | grep -E '✓|passed' | head -40 | sed 's/^/  | /'
else
	echo "$e2e" | tail -120 | sed 's/^/  | /'
fi
check "e2e XSS: every page shows the strings as text, nothing ran" grep -qE '^ *[0-9]+ passed' <<<"$e2e"
check "e2e XSS: no test failed" not grep -qE '[0-9]+ failed' <<<"$e2e"
check "e2e a11y: axe finds nothing serious on any page, both themes, LTR and RTL" grep -qE '✓.*a11y\.spec\.ts.*axe: every page, light, right to left' <<<"$e2e"
check "e2e a11y: focus rings, palette, shortcuts and a confirmation work by keyboard" grep -qE '✓.*a11y\.spec\.ts.*a confirmation is keyboard operable' <<<"$e2e"
check "e2e: a managed app's drawer shows its spec as text" grep -qE "✓.*xss\.spec\.ts.*a managed app's drawer" <<<"$e2e"
check "e2e a11y: axe finds nothing serious in an app's drawer" grep -qE "✓.*a11y\.spec\.ts.*axe: an app's drawer, light, right to left" <<<"$e2e"
rm -rf /var/lib/hearth/stacks
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
check "charge limit policy is present" grep -q 'org.hearth.battery.threshold' "$policy"
for bad in '{"battery":"BAT0","end":49}' '{"battery":"BAT0","end":101}' '{"battery":"../../x","end":80}' '{"battery":"NOPE","end":80}' '{"battery":"BAT0","end":80,"x":1}'; do
	check "battery.threshold refused before polkit: $bad" call_says "bad params\|must be\|no such battery\|not a battery" battery.threshold "$bad"
done
check "the sleep route answers" grep -q '"inhibitors":' <<<"$(pkgs_api power/sleep)"
conn_put() { curl -s -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' -d "$1" "http://127.0.0.1:$port/api/v1/connectivity/config"; }
check "connectivity is off by default" grep -q '"enabled":false' <<<"$(pkgs_api connectivity)"
check "no check ran before it was turned on" grep -q '"checks":\[\]' <<<"$(pkgs_api connectivity)"
check "a URL is no connectivity target" test "$(conn_put '{"enabled":true,"targets":[{"host":"http://evil.test"}]}')" = 400
check "turning it on names a target" test "$(conn_put '{"enabled":true,"targets":[]}')" = 400
check "connectivity needs the session key" test "$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "Origin: http://127.0.0.1:$port" -d '{}' "http://127.0.0.1:$port/api/v1/connectivity/config")" = 401
check "connectivity saves" test "$(conn_put '{"enabled":false,"targets":[{"host":"127.0.0.1","port":'"$port"'}]}')" = 200
check "the save is audited" grep -q 'connectivity.configure' <<<"$(pkgs_api changes)"
check "the hardware route answers" grep -q '"pci":' <<<"$(pkgs_api hardware)"
check "the firmware route says what it found" grep -q '"available":' <<<"$(pkgs_api hardware/firmware)"
check "the forecast alert exists" grep -q '"metric":"disk.forecast_full"' <<<"$(pkgs_api alerts/rules)"
check "the forecast route answers" grep -q '"disks":' <<<"$(pkgs_api forecasts)"
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

echo "== backup"
preview_import() {
	jq -n --rawfile f "$1" '{target:"settings",params:{file:$f},preview:true}' |
		curl -s -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' \
			-d @- "http://127.0.0.1:$port/api/v1/actions/hearth/import"
}
post_action() {
	curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' \
		-d "$2" "http://127.0.0.1:$port/api/v1/actions/hearth/$1"
}
check "a database copy is made on demand" test "$(post_action backup '{"target":"database"}')" = 200
data=$(asuser sh -c 'echo "${XDG_DATA_HOME:-$HOME/.local/share}"')/hearth
check "copies are private" test "$(stat -c '%a' "$data/backups")" = 700
copy=$(ls -1 "$data"/backups/hearth-*.db | tail -1)
check "and each one is 0600" test "$(stat -c '%a' "$copy")" = 600
check "the copy is a sound database" test "$(sqlite3 "$copy" 'PRAGMA integrity_check')" = ok
export_file=$(mktemp)
pkgs_api backup/export >"$export_file"
chmod 644 "$export_file"
check "the export has the alert rules" grep -q 'Disk almost full' "$export_file"
check "the export has no session key" not grep -qF "$key" "$export_file"
check "re-importing an export changes nothing" grep -q '"items":\[\]' <<<"$(preview_import "$export_file")"
echo '{"format":"hearth-settings","version":1,"tiles":[{"name":"T","url":"javascript:alert(1)"}]}' >"$export_file"
check "a hostile tile is blocked, not applied" grep -q '"reason":"invalid"' <<<"$(preview_import "$export_file")"
check "an import needs a preview token" test "$(post_action import '{"target":"settings","params":{"file":"{}"}}')" = 409
rm -f "$export_file"

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

echo "== automations (12.10)"
# Gate 12 through the real daemon, systemd, polkit and PackageKit. Every
# trigger kind on a simulated clock is internal/automations/gate12_test.go;
# here real events fire real automations.
key=$(api_key)
home=/home/$user
hearth_db=$home/.local/share/hearth/hearth.db
code() { # code METHOD path [body] -> the status
	curl -s -o /dev/null -w '%{http_code}' -X "$1" -H "Authorization: Bearer $key" -H "Origin: http://127.0.0.1:$port" \
		-H 'Content-Type: application/json' --data-binary @- "http://127.0.0.1:$port/api/v1/$2" <<<"${3:-}"
}
act() { # act kind/name target -> the result; previews first and passes the token when one is given
	local tok
	tok=$(post "actions/$1" "{\"target\":\"$2\",\"preview\":true}" | jq -r '.token // empty')
	post "actions/$1" "$(jq -nc --arg t "$2" --arg k "$tok" '{target:$t} + (if $k == "" then {} else {token:$k} end)')"
}
ftrig() { printf '{"kind":"files","folder":"~/%s","event":"new","depth":0,"pattern":"*"}' "$1"; }
step() { printf '{"step":"action","action":"%s","target":"%s"}' "$1" "$2"; }
aspec() { printf '{"trigger":%s,"then":[%s],"options":{"cooldown_s":60,"max_per_hour":60}}' "$1" "$2"; }
abody() { jq -nc --arg n "$1" --argjson s "$2" '{name:$n,enabled:true,spec:$s}'; }
mk() { # mk name spec -> id
	local out
	out=$(post automations "$(abody "$1" "$2")")
	jq -e '.id' <<<"$out" 2>/dev/null || echo "  save of $1 refused: $(cut -c1-200 <<<"$out")" >&2
}
runs_of() { pkgs_api "automations/$1/runs" | jq -r '[.runs[] | .state + (if .record.reason then "(" + .record.reason + ")" else "" end)] | sort | join(" ")'; }
runs_are() { test "$(runs_of "$1")" = "$2"; }
runs_have() { grep -q -- "$2" <<<"$(runs_of "$1")"; }
steps_of() { pkgs_api "automations/$1/runs" | jq -c '[.runs[].record.steps[] | .result + ":" + (.detail // "")]'; }
ran() { find /tmp/itest-auto -name 'ran.*' | wc -l; }
ran_is() { test "$(ran)" = "$1"; }
sys_pid() { systemctl show -p MainPID --value itest-auto-sys.service; }
approval_of() { pkgs_api automations/approvals | jq -r --argjson a "$1" '[.. | objects | select(.automation_id? == $a) | .id][0] // empty'; }
# "action flags" of each CheckAuthorization dbus-monitor printed: the
# call's own arguments are indented by three spaces.
polkit_calls() {
	awk '/^method call/ { a = "" } /^   string "/ { if (a == "") a = $2 } /^   uint32 / { gsub(/"/, "", a); print a, $2 }' /run/itest-polkit.txt
}
polkit_has() { grep -q -- "$1" <<<"$(polkit_calls)"; }
count_of() { sqlite3 "$hearth_db" "SELECT count(*) FROM $1"; }

asuser mkdir -p "$home/Inbox" "$home/Priv" "$home/Drop" "$home/Junk" "$home/Prot" "$home/Loop" "$home/Flood" /tmp/itest-auto
printf '[Service]\nType=oneshot\nExecStart=/usr/bin/mktemp /tmp/itest-auto/ran.XXXXXX\n' >/etc/systemd/user/itest-auto.service
printf '[Service]\nType=oneshot\nExecStart=/usr/bin/mktemp %s/Loop/loop.XXXXXX\n' "$home" >/etc/systemd/user/itest-loop.service
for n in 1 2; do
	printf '[Service]\nType=oneshot\nExecStart=/bin/true\n' >/etc/systemd/user/itest-noop$n.service
done
printf '[Service]\nExecStart=/bin/sleep 600\n' >/etc/systemd/system/itest-auto-sys.service
systemctl daemon-reload
asuser systemctl --user daemon-reload
systemctl start itest-auto-sys.service
# Every CheckAuthorization on the system bus is recorded with its flags (1
# is "the user may be asked"), which sees more than an agent would. The
# control comes first: a click in the UI does ask, and is refused here,
# where nobody can answer.
systemd-run --quiet --unit=itest-polkit-mon -p StandardOutput=file:/run/itest-polkit.txt \
	/usr/bin/dbus-monitor --system "type='method_call',interface='org.freedesktop.PolicyKit1.Authority',member='CheckAuthorization'"
sleep 1
interactive() { polkit_calls | grep -c ' 1$' || true; }
pid0=$(sys_pid)
out=$(post actions/unit/restart '{"target":"system/itest-auto-sys.service"}')
echo "  attended restart, nobody to answer: $(cut -c1-160 <<<"$out")"
check "an attended restart of a system unit asks polkit interactively" wait_until 5 polkit_has 'manage-units 1$'
check "and is refused without an answer" test "$(sys_pid)" = "$pid0"
asked=$(interactive)
units_asked=$(polkit_calls | grep -c manage-units || true)
# From here polkit would let the test user manage units without a
# password, and says when it is asked: an unattended run must not even ask.
cat >/etc/polkit-1/rules.d/54-hearth-itest-auto.rules <<'RULES'
polkit.addRule(function(action, subject) {
	if (action.id == "org.freedesktop.systemd1.manage-units" && subject.user == "tester") {
		polkit.spawn(["/usr/bin/logger", "-t", "hearth-itest", "auto " + action.id + " unit=" + action.lookup("unit")]);
		return polkit.Result.YES;
	}
});
RULES
systemd-run --quiet --unit=itest-notify-mon --uid="$user" -p StandardOutput=file:/run/itest-notify.jsonl \
	/usr/bin/stdbuf -oL /usr/bin/busctl monitor --address="unix:path=/run/user/$uid/bus" --json=short \
	--match "type='method_call',interface='org.freedesktop.Notifications',member='Notify'"
sleep 2 # polkitd reloads its rules; the monitors attach
check "the pause switch is off" grep -q '"paused":false' <<<"$(pkgs_api automations/pause)"

# A real trigger, and its cooldown.
a1=$(mk "itest inbox" "$(aspec "$(ftrig Inbox)" "$(step unit/start user/itest-auto.service)")")
check "an automation saves" test -n "$a1"
sleep 1
asuser touch "$home/Inbox/a"
check "a new file runs it: the unit started" wait_until 10 ran_is 1
check "the run is recorded as done" wait_until 5 runs_are "$a1" "done"
asuser touch "$home/Inbox/b"
check "a second file within the cooldown is seen and skipped" wait_until 10 runs_are "$a1" "done skipped(cooldown)"
check "and starts nothing" ran_is 1
echo "  runs: $(runs_of "$a1")"

# A privileged step and a destructive step wait; nothing after them runs.
pid0=$(sys_pid)
a2=$(mk "itest privileged" "$(aspec "$(ftrig Priv)" "$(step unit/restart system/itest-auto-sys.service),$(step unit/start user/itest-auto.service)")")
asuser touch "$home/Junk/old.iso"
a3=$(mk "itest destructive" "$(aspec "$(ftrig Drop)" '{"step":"action","action":"files/trash","target":"~/Junk","params":{"pattern":"*.iso","days":"0"}},'"$(step unit/start user/itest-auto.service)")")
check "both save" test -n "$a2" -a -n "$a3"
sleep 1
asuser touch "$home/Priv/a" "$home/Drop/a"
check "a privileged step waits for approval" wait_until 10 runs_are "$a2" "waiting"
check "a destructive step waits for approval" wait_until 10 runs_are "$a3" "waiting"
sleep 2
check "the system unit was not restarted" test "$(sys_pid)" = "$pid0"
check "the file was not trashed" test -f "$home/Junk/old.iso"
check "no step after a waiting one ran" ran_is 1
ap2=$(approval_of "$a2")
ap3=$(approval_of "$a3")
check "both approvals are listed" test -n "$ap2" -a -n "$ap3"
echo "  waiting: $(steps_of "$a2") $(steps_of "$a3")"

# Protected targets and the deny list, at save.
refused_save() { # refused_save step-json
	local out
	out=$(post automations "$(abody "itest refused" "$(aspec "$(ftrig Inbox)" "$1")")")
	echo "    $(jq -c '.error // .' <<<"$out" | cut -c1-140)" >>/run/itest-refusals.txt
	grep -q '"error"' <<<"$out"
}
for s in "unit/stop system/dbus.service" "unit/restart system/polkit.service" "unit/stop user/hearthd.service" \
	"unit/mask system/hearth-broker.service" "package/remove bash" "package/remove hearth" \
	"uninstall/remove pkg:bash" "hearth/import x" "package/update-all all" "automation/approve 1" "sh/c id"; do
	check "refused at save: $s" refused_save "$(step ${s% *} "${s#* }")"
done
cat /run/itest-refusals.txt
check "refused at save: continue on a step that may ask" refused_save '{"step":"action","action":"unit/restart","target":"system/itest-auto-sys.service","continue":true}'

# A target that becomes protected after the save is refused at the run,
# from live data: it fails, it does not wait for an approval.
pdeb() { # pdeb version [control line]
	rm -rf /tmp/pdeb && mkdir -p /tmp/pdeb/DEBIAN
	printf 'Package: itest-auto-pkg\nVersion: %s\nArchitecture: all\nMaintainer: itest <itest@example.invalid>\n%sDescription: itest automation sample\n' "$1" "${2:-}" >/tmp/pdeb/DEBIAN/control
	dpkg-deb --build /tmp/pdeb "/tmp/itest-auto-pkg_$1.deb" >/dev/null && dpkg -i "/tmp/itest-auto-pkg_$1.deb" >/dev/null 2>&1
}
pdeb 1.0
a4=$(mk "itest package" "$(aspec "$(ftrig Prot)" "$(step package/remove itest-auto-pkg)")")
check "removing an ordinary package saves" test -n "$a4"
pdeb 1.1 $'Protected: yes\n'
check "the package is now Protected" grep -q yes <<<"$(dpkg-query -W -f '${Protected}' itest-auto-pkg)"
sleep 1
asuser touch "$home/Prot/a"
check "refused at run: the run fails" wait_until 20 runs_are "$a4" "failed"
echo "  $(steps_of "$a4")"
check "the step says refused" grep -q '"failed:refused: ' <<<"$(steps_of "$a4")"
check "it does not wait for an approval" test -z "$(approval_of "$a4")"
check "the package is still installed" dpkg -s itest-auto-pkg

# So far every run was unattended, and every save was checked.
echo "  polkit was asked: $(polkit_calls | sort | uniq -c | tr '\n' ';')"
check "the recorder is running" systemctl is-active itest-polkit-mon.service
check "no unattended run or save asked polkit interactively" test "$(interactive)" = "$asked"
check "polkit was not asked about units again at all" test "$(polkit_calls | grep -c manage-units || true)" = "$units_asked"
check "its rule saw nothing either" not polkit_logged 'auto org.freedesktop.systemd1.manage-units'

# Dismissing runs nothing; approving runs the rest, with the user there.
out=$(act automation/dismiss "$ap3")
echo "  dismiss: $(cut -c1-160 <<<"$out")"
check "dismiss" grep -q '"done":[1-9]' <<<"$out"
check "the dismissed run is skipped" wait_until 5 runs_have "$a3" skipped
check "the file is still there" test -f "$home/Junk/old.iso"
out=$(act automation/approve "$ap2")
echo "  approve: $(cut -c1-160 <<<"$out")"
check "approve" grep -q '"done":[1-9]' <<<"$out"
restarted() { test "$(sys_pid)" != "$pid0" && test "$(sys_pid)" != 0; }
check "the approved step restarts the system unit" wait_until 15 restarted
check "and the step after it runs" wait_until 10 ran_is 2
check "the run is done" wait_until 5 runs_are "$a2" "done"
check "a second approve is refused" not grep -q '"done":[1-9]' <<<"$(act automation/approve "$ap2")"
# The control: the same recorder does see an attended request.
# The user is there now: the approved run may ask, and does.
asked_again() { test "$(interactive)" -gt "$asked"; }
check "the approved run asked polkit, interactively" wait_until 5 asked_again
attended=$(interactive)
echo "  polkit was asked: $(polkit_calls | sort | uniq -c | tr '\n' ';')"
check "and polkit's rule saw it" wait_until 5 polkit_logged 'auto org.freedesktop.systemd1.manage-units unit=itest-auto-sys.service'

# The dry run reports and executes nothing.
runs_n=$(count_of automation_runs)
audit_n=$(count_of audit_log)
dry=$(post automations/dry-run "{\"spec\":$(aspec '{"kind":"schedule","calendar":"hourly"}' "$(step unit/start user/itest-auto.service),$(step unit/restart system/itest-auto-sys.service)")}")
echo "  dry run: $(jq -c '{total, runs, trigger, steps: [.steps[] | .outcome]}' <<<"$dry" | cut -c1-200)"
check "an hourly schedule fired 167 or 168 times in 7 days" jq -e '.total >= 167 and .total <= 168 and .runs == .total' <<<"$dry"
check "its steps: one runs by itself, one would wait" jq -e '[.steps[].outcome] == ["unattended", "approval"]' <<<"$dry"
check "a dry run records no run" test "$(count_of automation_runs)" = "$runs_n"
check "and audits nothing" test "$(count_of audit_log)" = "$audit_n"
check "and starts nothing" ran_is 2

# A loop stops at its cooldown: the unit it starts makes the next file.
a5=$(mk "itest loop" "$(aspec "$(ftrig Loop)" "$(step unit/start user/itest-loop.service)")")
sleep 1
asuser touch "$home/Loop/seed"
# Its file arrives while the run goes on, or just after: skipped either way.
loop_stopped() { grep -Eq '^done skipped\((cooldown|a run is still going)\)$' <<<"$(runs_of "$a5")"; }
check "the loop's own file is seen and skipped" wait_until 10 loop_stopped
sleep 3
echo "  runs: $(runs_of "$a5")"
check "and the loop ends there" test "$(ls "$home/Loop" | wc -l)" = 2
check "with no further run" loop_stopped
asuser touch "$home/Loop/more"
check "another file within the cooldown is skipped by it" wait_until 10 runs_have "$a5" 'skipped(cooldown)'

# Hostile imports store nothing.
list_n() { pkgs_api automations | jq '.automations | length'; }
before=$(list_n)
imp() { printf '{"version":1,"automations":[{"name":"x","spec":%s}]}' "$1"; }
wait1='[{"step":"wait","seconds":1}]'
check "import: an unknown action" test "$(code POST automations/import "$(imp '{"trigger":{"kind":"login"},"then":[{"step":"action","action":"sh/c","target":"rm -rf ~"}]}')")" = 400
check "import: a denied action" test "$(code POST automations/import "$(imp '{"trigger":{"kind":"login"},"then":[{"step":"action","action":"uninstall/remove","target":"pkg:bash"}]}')")" = 400
check "import: a command in a trigger" test "$(code POST automations/import "$(imp "{\"trigger\":{\"kind\":\"login\",\"exec\":\"/bin/sh -c id\"},\"then\":$wait1}")")" = 400
check "import: a schedule that is a command" test "$(code POST automations/import "$(imp "{\"trigger\":{\"kind\":\"schedule\",\"calendar\":\"daily; reboot\"},\"then\":$wait1}")")" = 400
check "import: a target that is a command" test "$(code POST automations/import "$(imp '{"trigger":{"kind":"login"},"then":[{"step":"action","action":"unit/start","target":"user/x.service; reboot"}]}')")" = 400
check "import: an option as a target" test "$(code POST automations/import "$(imp '{"trigger":{"kind":"login"},"then":[{"step":"action","action":"unit/start","target":"--no-block"}]}')")" = 400
check "import: a folder outside the home" test "$(code POST automations/import "$(imp "{\"trigger\":{\"kind\":\"files\",\"folder\":\"~/../../etc\",\"event\":\"new\",\"depth\":0,\"pattern\":\"*\"},\"then\":$wait1}")")" = 400
check "import: a script field" test "$(code POST automations/import "$(imp "{\"trigger\":{\"kind\":\"login\"},\"then\":$wait1,\"script\":\"curl x | sh\"}")")" = 400
check "import: a check inside a check" test "$(code POST automations/import "$(imp '{"trigger":{"kind":"login"},"then":[{"step":"check","what":"unit_state","target":"x.service","expect":"active","then":[{"step":"check","what":"unit_state","target":"x.service","expect":"active","then":[{"step":"wait","seconds":1}]}]}]}')")" = 400
deep=$(printf '{"step":"check","what":"unit_state","target":"x","expect":"active","then":[%.0s' $(seq 200))
check "import: 200 levels deep" test "$(code POST automations/import "$(imp "{\"trigger\":{\"kind\":\"login\"},\"then\":[$deep]}")")" = 400
check "import: 100,000 brackets" test "$(code POST automations/import "$(head -c 100000 /dev/zero | tr '\0' '[')")" = 400
big=$(head -c 70000 /dev/zero | tr '\0' 'a')
check "import: oversized" test "$(code POST automations/import "$(imp "{\"trigger\":{\"kind\":\"login\"},\"then\":$wait1,\"pad\":\"$big\"}")")" = 400
check "import: another version" test "$(code POST automations/import '{"version":2,"automations":[]}')" = 400
check "the refused imports stored nothing" test "$(list_n)" = "$before"
exported=$(pkgs_api automations/export)
check "an export imports" test "$(code POST automations/import "$(jq -c '.automations |= (map(select(.name == "itest inbox")) | map(.enabled = true))' <<<"$exported")")" = 200
check "and every imported automation starts turned off" jq -e '[.automations[] | select(.origin == "import")] | length > 0 and all(.enabled == false)' <<<"$(pkgs_api automations)"

# The global limit pauses everything and says so; nothing runs while paused.
noops() { for _ in $(seq 16); do step unit/start "user/itest-noop$1.service"; done | sed 's/}{/},{/g'; }
a6=$(mk "itest flood 1" "$(aspec "$(ftrig Flood)" "$(noops 1)")")
a7=$(mk "itest flood 2" "$(aspec "$(ftrig Flood)" "$(noops 2)")")
check "two automations of 16 steps save" test -n "$a6" -a -n "$a7"
sleep 1
asuser touch "$home/Flood/a"
paused() { grep -q '"paused":true' <<<"$(pkgs_api automations/pause)"; }
check "more than 30 actions in a minute pause every automation" wait_until 30 paused
echo "  runs: $(runs_of "$a6") | $(runs_of "$a7")"
check "one of the runs was stopped by the limit" grep -q 'limited' <<<"$(runs_of "$a6") $(runs_of "$a7")"
check "the pause is in the audit log, with its reason" grep -q 'paused=true by global limit' <<<"$(sqlite3 "$hearth_db" "SELECT result FROM audit_log WHERE action='automations.pause'")"
check "and the desktop is told" wait_until 5 grep -q 'Automations paused' /run/itest-notify.jsonl
asuser touch "$home/Inbox/c" "$home/Loop/again"
check "while paused a trigger is seen and skipped" wait_until 10 runs_have "$a1" 'skipped(all automations are paused)'
sleep 2
check "and starts nothing" ran_is 2
check "nor does the loop" test "$(ls "$home/Loop" | wc -l)" = 4
check "the pause is the user's to lift" test "$(code PUT automations/pause '{"paused":false}')" = 200
check "it is off again" not paused
check "the kill switch turns on" test "$(code PUT automations/pause '{"paused":true}')" = 200
asuser touch "$home/Prot/b"
check "and stops a run" wait_until 10 runs_have "$a4" 'skipped(all automations are paused)'
check "and turns off" test "$(code PUT automations/pause '{"paused":false}')" = 200
check "no interactive polkit request since the approved run" test "$(interactive)" = "$attended"

for id in $(pkgs_api automations | jq -r '.automations[] | select(.name | startswith("itest ")) | .id'); do
	code DELETE "automations/$id" >/dev/null
done
check "the test's automations are deleted" test "$(pkgs_api automations | jq '[.automations[] | select(.name | startswith("itest "))] | length')" = 0
systemctl stop itest-polkit-mon.service itest-notify-mon.service itest-auto-sys.service 2>/dev/null || true
dpkg --purge --force-remove-protected itest-auto-pkg >/dev/null 2>&1 || true
rm -f /etc/polkit-1/rules.d/54-hearth-itest-auto.rules /etc/systemd/user/itest-auto.service /etc/systemd/user/itest-loop.service \
	/etc/systemd/user/itest-noop1.service /etc/systemd/user/itest-noop2.service /etc/systemd/system/itest-auto-sys.service /run/itest-polkit.txt /run/itest-notify.jsonl /run/itest-refusals.txt
rm -rf "$home/Inbox" "$home/Priv" "$home/Drop" "$home/Junk" "$home/Prot" "$home/Loop" "$home/Flood" /tmp/itest-auto /tmp/pdeb /tmp/itest-auto-pkg_*.deb
systemctl daemon-reload
asuser systemctl --user daemon-reload

echo "== purge"
# A schedule's timer (9.11), as the broker would leave it: purge removes it.
printf '[Timer]\nOnCalendar=daily\nUnit=hearth-backup-scheduled@probe.service\n' >/etc/systemd/system/hearth-backup-probe.timer
# An app backup (9.10): purge keeps it, and only it.
mkdir -p -m 0700 /var/lib/hearth/backups/probe
echo archive >/var/lib/hearth/backups/probe/20261106T000000Z.tar.zst
touch /var/lib/hearth/backups/probe/.plan.json
# apt may hand the scripts' messages to stdout (a pty), so read both.
purge_out=$(apt-get purge -y -qq hearth 2>&1 || true)
check "purge removes backup timers" not test -e /etc/systemd/system/hearth-backup-probe.timer
check "purge keeps app backups" test -f /var/lib/hearth/backups/probe/20261106T000000Z.tar.zst
check "and says so" grep -q 'app backups kept in /var/lib/hearth/backups' <<<"$purge_out"
check "purge keeps uninstall archives" test -n "$(ls /var/lib/hearth/removed/*/*.tar.zst 2>/dev/null)"
check "and says so too" grep -q 'uninstall archives kept in /var/lib/hearth/removed' <<<"$purge_out"
check "and keeps nothing else" test "$(ls -A /var/lib/hearth | tr '\n' ' ')" = "backups removed "
check "and drops the units' plans" not test -e /var/lib/hearth/backups/probe/.plan.json
rm -rf /var/lib/hearth
check "purge removes the Lynis schedule" not test -e /etc/systemd/system/hearth-lynis.timer
check "and its enable link" not test -L /etc/systemd/system/timers.target.wants/hearth-lynis.timer
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
	/usr/lib/systemd/system/hearth-pathcheck.socket /usr/lib/systemd/system/hearth-pathcheck@.service \
	/usr/lib/systemd/system/hearth-backup@.service /usr/lib/systemd/system/hearth-restore@.service \
	/usr/lib/systemd/system/hearth-backup-scheduled@.service \
	/etc/systemd/system/sockets.target.wants/hearth-broker.socket \
	/etc/systemd/system/sockets.target.wants/hearth-pathcheck.socket /run/hearth; do
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

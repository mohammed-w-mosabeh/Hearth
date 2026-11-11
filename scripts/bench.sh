#!/usr/bin/env bash
# Resource budget check for an idle hearthd (CLAUDE.md "Resource budget").
# Runs bin/hearthd with throwaway XDG dirs on 127.0.0.1, samples RSS and CPU
# once a second, and fails if the idle budget is exceeded.
set -euo pipefail

bin=${1:-bin/hearthd}
seconds=${BENCH_SECONDS:-60}
settle=${BENCH_SETTLE:-70}
port=${BENCH_PORT:-7391}
max_rss_kb=${BENCH_MAX_RSS_KB:-40960} # 40 MB
max_cpu_pct=${BENCH_MAX_CPU_PCT:-1.0}
# BENCH_AUTOMATIONS=N measures with N automations turned on (Gate 12 asks
# for 20), one of each trigger kind in turn, so every source they wake is
# running: the file watch, the SSH count, the app and game scans, idle.
automations=${BENCH_AUTOMATIONS:-0}

tmp=$(mktemp -d)
pid=
cleanup() {
	if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
	rm -rf "$tmp"
}
trap cleanup EXIT

# A pre-written config means "not first run": hearthd will not open a browser.
mkdir -p -m 700 "$tmp/config/hearth" "$tmp/data"
printf '{"port": %d}\n' "$port" >"$tmp/config/hearth/config.json"
chmod 600 "$tmp/config/hearth/config.json"

# No session bus: default alert rules (e.g. a disk over 90%) must not pop
# desktop notifications during a benchmark.
DBUS_SESSION_BUS_ADDRESS=unix:path=$tmp/no-bus \
	XDG_CONFIG_HOME=$tmp/config XDG_DATA_HOME=$tmp/data "$bin" 2>"$tmp/log" &
pid=$!

for _ in $(seq 50); do
	curl -fsS "http://127.0.0.1:$port/api/v1/health" >/dev/null 2>&1 && break
	sleep 0.1
done
curl -fsS "http://127.0.0.1:$port/api/v1/health" >/dev/null || {
	echo "hearthd did not start:"
	cat "$tmp/log"
	exit 1
}

if [ "$automations" -gt 0 ]; then
	uuid=0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11
	triggers=(
		'{"kind":"schedule","calendar":"*-*-* 03:30"}'
		'{"kind":"login"}'
		'{"kind":"metric","metric":"cpu.temp","op":">","threshold":90}'
		'{"kind":"container","event":"unhealthy"}'
		'{"kind":"unit_failed"}'
		'{"kind":"new_listener"}'
		'{"kind":"usb_device"}'
		'{"kind":"integrity_changed"}'
		'{"kind":"internet","state":"down"}'
		'{"kind":"security_updates"}'
		'{"kind":"image_update"}'
		'{"kind":"ssh_failed","count":10}'
		'{"kind":"power","event":"unplugged"}'
		"{\"kind\":\"wifi\",\"uuid\":\"$uuid\",\"state\":\"up\"}"
		"{\"kind\":\"vpn\",\"uuid\":\"$uuid\",\"state\":\"up\"}"
		'{"kind":"idle","minutes":30}'
		'{"kind":"display","event":"connected"}'
		'{"kind":"app","state":"started","game":true}'
		'{"kind":"resume"}'
		'{"kind":"files","folder":"~/Downloads","event":"new","depth":0,"pattern":"*"}'
	)
	key=$(curl -s -o /dev/null -w '%{redirect_url}' "http://127.0.0.1:$port/auth?t=$(cat "$tmp/config/hearth/token")" | sed 's/.*#k=//')
	for i in $(seq "$automations"); do
		t=${triggers[$(((i - 1) % ${#triggers[@]}))]}
		# The step only waits: an automation that fires changes nothing.
		code=$(curl -s -o "$tmp/reply" -w '%{http_code}' -X POST -H "Authorization: Bearer $key" \
			-H "Origin: http://127.0.0.1:$port" -H 'Content-Type: application/json' \
			-d "{\"name\":\"bench $i\",\"enabled\":true,\"spec\":{\"trigger\":$t,\"then\":[{\"step\":\"wait\",\"seconds\":1}]}}" \
			"http://127.0.0.1:$port/api/v1/automations")
		if [ "$code" != 200 ]; then
			echo "automation $i ($t) was not saved: $code $(cat "$tmp/reply")"
			exit 1
		fi
	done
	echo "$automations automations turned on"
fi

# Let startup settle before measuring. Throwaway dirs mean a first run, so the
# integrity watch hashes every watched file for its baseline one minute in
# (0.9 s of CPU on 265 MB, once ever). That is setup, not idle: wait past it.
sleep "$settle"

ticks() { awk '{print $14 + $15}' "/proc/$pid/stat"; } # utime + stime
rss_kb() { awk '/^VmRSS:/ {print $2}' "/proc/$pid/status"; }

hz=$(getconf CLK_TCK)
start_ticks=$(ticks)
start_ns=$(date +%s%N)
peak=0
sum=0
for _ in $(seq "$seconds"); do
	sleep 1
	r=$(rss_kb)
	sum=$((sum + r))
	[ "$r" -gt "$peak" ] && peak=$r
done
end_ticks=$(ticks)
end_ns=$(date +%s%N)

awk -v t0="$start_ticks" -v t1="$end_ticks" -v hz="$hz" \
	-v ns0="$start_ns" -v ns1="$end_ns" -v peak="$peak" -v sum="$sum" -v n="$seconds" \
	-v max_rss="$max_rss_kb" -v max_cpu="$max_cpu_pct" -v autos="$automations" '
BEGIN {
	wall = (ns1 - ns0) / 1e9
	cpu = ((t1 - t0) / hz) / wall * 100
	printf "hearthd idle over %.0f s%s\n", wall, (autos > 0 ? " with " autos " automations on" : "")
	printf "  RSS  peak %.1f MB, avg %.1f MB   (budget < %.0f MB)\n", peak / 1024, sum / n / 1024, max_rss / 1024
	printf "  CPU  avg %.2f %%                   (budget < %.1f %%)\n", cpu, max_cpu
	ok = 1
	if (peak >= max_rss) { print "FAIL RSS over budget"; ok = 0 }
	if (cpu >= max_cpu) { print "FAIL CPU over budget"; ok = 0 }
	if (ok) print "PASS idle budget"
	exit !ok
}'

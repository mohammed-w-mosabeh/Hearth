#!/usr/bin/env bash
# Resource budget check for an idle hearthd (CLAUDE.md "Resource budget").
# Runs bin/hearthd with throwaway XDG dirs on 127.0.0.1, samples RSS and CPU
# once a second, and fails if the idle budget is exceeded.
set -euo pipefail

bin=${1:-bin/hearthd}
seconds=${BENCH_SECONDS:-60}
port=${BENCH_PORT:-7391}
max_rss_kb=${BENCH_MAX_RSS_KB:-40960} # 40 MB
max_cpu_pct=${BENCH_MAX_CPU_PCT:-1.0}

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

# Let startup allocations settle before measuring.
sleep 2

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
	-v max_rss="$max_rss_kb" -v max_cpu="$max_cpu_pct" '
BEGIN {
	wall = (ns1 - ns0) / 1e9
	cpu = ((t1 - t0) / hz) / wall * 100
	printf "hearthd idle over %.0f s\n", wall
	printf "  RSS  peak %.1f MB, avg %.1f MB   (budget < %.0f MB)\n", peak / 1024, sum / n / 1024, max_rss / 1024
	printf "  CPU  avg %.2f %%                   (budget < %.1f %%)\n", cpu, max_cpu
	ok = 1
	if (peak >= max_rss) { print "FAIL RSS over budget"; ok = 0 }
	if (cpu >= max_cpu) { print "FAIL CPU over budget"; ok = 0 }
	if (ok) print "PASS idle budget"
	exit !ok
}'

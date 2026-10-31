# Hearth — Product Spec

Single-machine (this Debian laptop) manager + monitor. Everything is auto-discovered on install; nothing has to be added by hand except Homepage entries (entered through a form, with auto-suggestions).

Priority: **P1** = v0.x MVP, **P2** = v1.0, **P3** = after v1.0.

## Shared concepts
- **Item**: anything discovered (app, tool, package, container, image, game, service, port...). Has a stable ID (`<kind>:<source>:<native-id>`), so user overrides (rename, description, hide, favorite, tags) survive rescans.
- **Action**: typed operation on an item with a `risk` level:
  - `safe` (start app, view logs) → no confirm
  - `reversible` (stop container, stop service) → no dialog, toast with Undo for 5 s
  - `destructive` (remove package, delete image, kill -9) → preview + confirm dialog (type the name for high-impact)
  - `privileged` → additionally requires polkit authentication
- **Audit log**: every action recorded (time, origin: local, action, target, result).

## Modules

### 1. Home & Quick Views (P1)
- Draggable card grid: CPU, RAM/swap, each disk, GPU, temperatures, battery, network throughput, latest alerts, Homepage tiles.
- Cards are hideable/reorderable; layout saved per user.

### 2. Applications (P1)
- Sources: `.desktop` files (`/usr/share/applications`, `~/.local/share/applications`, Flatpak exports, `/var/lib/snapd/desktop/applications`), AppImages in `~/Applications`, `~/Downloads`, `~/.local/bin`.
- Grid with icons, search, filter by source/category, favorites.
- Actions: launch (as a transient user service `app-hearth-<id>@<n>.service`, started by the systemd user manager over D-Bus, so usage is measurable and stoppable as a group and the app does not inherit hearthd's sandbox), close, uninstall (via owning package manager), open containing folder.

### 3. Tools (P2)
- Every executable in `$PATH` (plus the usual user and manager bin directories) with: owning package (dpkg's file lists, or the Flatpak/Snap/pip/npm/cargo/Go listing; cached), version (the owner's version, or a Go binary's build info), short description (`whatis`/`apropos`).
- Listed programs are never run to learn about them (SECURITY S7): no `--version`.
- Actions: open in terminal, open man page, remove owning package.

### 4. Packages (P1)
- Managers: apt/dpkg, Flatpak, Snap, pip (user), npm -g, cargo, go bin.
- Columns: name, version, size, source repo, manual/auto, held, update available (security flagged).
- Actions: install, remove, purge, update, update all, hold/unhold, show files/deps/reverse-deps.
- Removal always simulated first; show the full list of what will be removed. Packages with `Priority: required` or `Essential: yes` cannot be removed from the UI.

### 5. Docker (P1)
- **Containers**: state, health, uptime, CPU/RAM/net/blkio with history, ports, mounts, networks, env (secrets masked), labels, restart policy. Actions: start, stop, restart, pause, remove (confirm), logs (live), exec shell (xterm), inspect JSON.
- **Images (detailed)**: repo:tag, ID, digest, size (unique vs shared), created, architecture/OS, default user, exposed ports, entrypoint/cmd, env, labels, full layer history with the command per layer, containers using it, "update available" (compare local digest with registry, on demand), optional Trivy vuln scan if installed. Actions: remove (blocked if in use unless forced with confirm), tag, pull latest.
- **Networks and volumes**: driver, project, used by, volume size; remove an orphan after a preview (the name is typed for a volume).
- **Compose projects**: grouped by `com.docker.compose.project` label; start/stop/restart whole project; show compose file path.
- **Networks & volumes**: list, size, used-by, remove orphans.
- **Cleanup**: dangling images, stopped containers, orphan volumes, build cache — show reclaimable size before acting.

### 6. Homepage (P1)
- Replaces gethomepage. Tiles entered via a **form**: name, URL (scheme, host/IP, port, path), description, icon (built-in icon set or upload), group, optional linked container, optional health check toggle.
- Each tile: status dot (up/down/unknown), latency, open link, start/stop linked container.
- **Suggestions**: published container ports and local HTTP listeners appear as grey "Add?" tiles with the form pre-filled. Nothing is added without the user saving the form.
- Health check: `GET`/`HEAD`, 5 s timeout, no cross-host redirects, every 60 s (only while enabled).

### 7. Games (P2)
- Steam (`libraryfolders.vdf`, `appmanifest_*.acf`), Lutris (`pga.db`), Heroic (JSON stores), `.desktop` with `Categories=Game`.
- Show cover, size on disk, last played, playtime (tracked by Hearth when launched through it). Launch Steam via `steam://rungameid/<id>`.

### 8. Resources & per-app usage (P1)
- CPU (total + per core, freq), load, RAM, swap, disk I/O, GPU (util, VRAM, temp, power, processes — NVML for NVIDIA, sysfs for AMD/Intel), temperatures (all hwmon/thermal sensors with labels), fans.
- **Processes grouped by application** (by cgroup/systemd scope or desktop app, fallback by executable): CPU, RAM, disk R/W, network, GPU, estimated power.
- Actions: terminate (SIGTERM), kill (SIGKILL, confirm), freeze/resume (SIGSTOP/SIGCONT), renice/ionice, set memory/CPU cap via cgroup.
- History retention: 1 s resolution in-memory for 10 min; 1 min for 7 days; 1 h for 1 year (SQLite, downsampled).

### 9. Disks & battery (P1)
- Disks: usage per mount, I/O speed, SMART (temp, reallocated sectors, power-on hours, SSD life %, short/long self-test and its result) via broker, folder size explorer.
- Battery (UPower): %, state, health (full vs design capacity), cycles, discharge rate W, time remaining, 7-day chart, power profile switch (power-profiles-daemon).

### 10. Services (P1)
- All systemd units (system + user): state, sub-state, enabled, memory/CPU from cgroup, description.
- Actions: start, stop, restart, enable, disable, mask/unmask, view journal, edit via drop-in override (never edit the original unit).
- Critical units (`dbus`, `systemd-*`, `NetworkManager`, `polkit`, `hearth*`, display manager) require double confirmation with a clear warning.

### 11. Network & ports (P1)
- Listening sockets: proto, address (highlight `0.0.0.0`/`::` as exposed), port, process, user, service, container.
  Other users' sockets name their service or container (from the socket's cgroup) and that unit's main process; the exact process only for the user's own sockets.
- Active connections, per-interface and per-app throughput, IPs, DNS, gateway, Wi-Fi SSID/signal, VPN state, firewall status (nftables/ufw).
- Actions: stop owning process/service/container, open in browser if HTTP.
- **Lineage view ("What is this?")**: port → process → systemd unit → package → container.

### 12. Settings (P2)
- Only via D-Bus: NetworkManager (Wi-Fi, connections), brightness, power profile, audio (PipeWire via `wpctl`), date/time zone (timedated), hostname (hostnamed), autostart apps, reboot/shutdown/suspend (logind).

### 13. Terminal (P1)
- xterm.js + PTY over WebSocket, multiple tabs, runs as the user. "Open terminal here" for folders, containers (`docker exec`), tools.

### 14. Scheduler (P2)
- Form: name, command or script, schedule (visual builder → systemd `OnCalendar`, or `OnBootSec`, `OnUnitActiveSec`), run as user or root (root requires polkit), timeout, notify on failure.
- Implemented as real systemd `.service` + `.timer` units (user units in `~/.config/systemd/user`, root units via broker in `/etc/systemd/system`, prefixed `hearth-job-`). `Persistent=true`.
- Run history from journal: start, duration, exit code, output. "Run now", pause, delete.

### 15. Alerts (P1)
- Rule = metric + condition + duration + severity (+ quiet hours). Hysteresis to avoid flapping. One notification per incident until resolved.
- Defaults: disk > 90%, RAM > 90% for 5 min, CPU > 95% for 3 min, CPU/GPU temp > 90°C for 1 min, battery < 15% discharging, SMART failing, container exited unexpectedly, unit failed, scheduled job failed, new listener on 0.0.0.0.
- Also: container unhealthy, out-of-memory kill in a container, log line matching a pattern (RE2).
- Channels: desktop notification (`org.freedesktop.Notifications`), optional ntfy URL, optional SMTP.

### 16. Reports (P2)
- Machine report (period): resource summary, top consumers, alerts, change timeline, disk/battery health, security posture.
- Item report: one app/container/service usage over time, peaks, restarts/crashes.
- Export: self-contained HTML, Markdown, PDF (browser print). Scheduled weekly report.

### 18. Unique features (P2 unless noted)
- **Command palette** Ctrl+K: search every item and action (P1).
- **Security posture**: exposed listeners, firewall state, SSH config (root login, password auth), pending security updates, `debsecan` CVEs, privileged containers, world-writable files in PATH.
- **Change timeline**: packages installed/removed, units changed, containers created/deleted, new listeners; diff two snapshots.
- **Profiles**: named sets of containers/services/apps to start/stop together ("Dev", "Gaming" — auto-stop heavy stuff when a game launches and restore after, "Battery").
- **Space advisor**: Docker leftovers, apt cache, journal size, old kernels, `~/.cache`, trash, large files — preview, then move to trash/clean.
- **"Why is my fan loud?"**: one-click explanation from top consumers + temps.
- **Per-app energy estimate**.
- **Sandbox run** (P3): run an unknown script/tool in a throwaway container with no network and a read-only bind of one folder.
- **Boot analysis**: `systemd-analyze blame` / critical-chain.
- **Journal viewer** with filters (unit, priority, time, text).

# Hearth — Roadmap

Rules: one task at a time, `make check` + commit after each, tick the box in the same commit. A phase ends only when its **gate** passes — report the gate results to the owner and wait for approval before the next phase.

## Phase 0 — Foundations (~1 week)
- [x] 0.1 `git init`, `go.mod` (module `github.com/mohammed-w-mosabeh/hearth`), `.gitignore`, `Makefile` with all targets from CLAUDE.md (stubs allowed), `.editorconfig`
- [x] 0.2 Lint/CI config: `golangci-lint` (incl. `gosec`, `depguard` forbidding `os/exec` outside `internal/safeexec`), custom check script for `sh -c` / `bash -c` / `{@html}`, `govulncheck`, GitHub Actions workflow (not pushed)
- [x] 0.3 `internal/safeexec` with binary allowlist + validators + fuzz tests
- [x] 0.4 `internal/store`: SQLite open (WAL, 0600), migrations runner, `audit_log` table
- [x] 0.5 `internal/auth`: token file (0600, rotate on start, single-use), `/auth?t=` exchange → bearer session key (SECURITY S2; no cookie), middleware
- [x] 0.6 `internal/api`: server on 127.0.0.1:<port>, Host/Origin middleware, security headers/CSP, rate limit, body limit, `/api/v1/health`, WebSocket hub with Origin check — with tests for every rejection path
- [x] 0.7 `web/`: Svelte 5 + Vite + TS skeleton, tokens.css (light/dark), sidebar layout, English UI strings in an i18n file (en.json), embedded via `go:embed`
- [x] 0.8 `cmd/hearthd`: refuses root, loads config, starts server, opens browser on first run (`xdg-open`)
- [x] 0.9 `packaging/`: hearthd user unit (hardened), nfpm config, desktop file; `make deb` builds an installable package (unsigned until signing is set up); `make itest` Debian 13 + systemd container
- [x] 0.10 `make bench` script: measures RSS/CPU of idle hearthd over 60 s

**Gate 0:** no listener off localhost (`ss -tlnp`), all auth/Origin/Host tests pass, `make check` clean, `.deb` installs and uninstalls cleanly in `make itest` container.

## Phase 1 — MVP: monitor and control (3–4 weeks)
- [x] 1.1 `collect`: CPU (total, per core, freq, load), RAM/swap — ring buffer + adaptive sampling (1 s with subscribers / 15 s otherwise)
- [x] 1.2 `store`: downsampler to `metrics_1m` / `metrics_1h` with retention
- [x] 1.3 `collect`: disks (usage per mount, I/O), network throughput per interface
- [x] 1.4 `collect`: sensors (hwmon/thermal) and fans; GPU (NVML if present via dlopen-free approach or `nvidia-smi` fallback through safeexec at ≥ 2 s; AMD/Intel sysfs)
- [x] 1.5 `collect`: battery via UPower D-Bus + power profiles
- [x] 1.6 WS topics `metrics` + Home page Quick Views cards with uPlot sparklines (draggable grid, layout persisted)
- [x] 1.7 Resources page: detailed charts with time range (10 min / 24 h / 7 d / 1 y)
- [x] 1.8 `collect/proc`: processes grouped by app (cgroup/scope → desktop app → exe), CPU/RAM/IO/net
  - Open: network per app needs privileges (eBPF or packet capture; no unprivileged per-process byte counters). Not shown until the owner decides (broker method in phase 2, or drop).
- [x] 1.9 Per-app actions: terminate, kill (confirm), freeze/resume, renice — with audit log
- [x] 1.10 `discovery`: `.desktop` apps (+ Flatpak/Snap exports, AppImages) with fsnotify; Applications page (grid, search, favorites)
- [x] 1.11 App launch via a transient user unit (D-Bus `StartTransientUnit`; a scope would inherit hearthd's sandbox) + close action
- [x] 1.12 `services`: list system + user units via D-Bus with signals; Services page
- [x] 1.13 Service actions (start/stop/restart/enable/disable) via systemd D-Bus (polkit handles auth); critical-unit double confirm; journal viewer for a unit
- [x] 1.14 `alerts`: rule engine with duration + hysteresis, default rules, desktop notification channel, Alerts page (rules form + history)
- [x] 1.15 Command palette Ctrl+K over apps, services, pages
- [x] 1.16 Disks page: usage, folder size explorer (user-owned paths)

**Gate 1:** `make bench` idle < 40 MB RSS and < 1% CPU; UI open < 60 MB / < 3%; bundle < 300 KB gz; security checklist items applicable so far pass.

Gate 1 result (2026-10-26): **passed**. Idle 21.7 MB / 0.07% CPU. UI open: Home 26.3 MB / 0.50%, per-app process view (scans /proc every second, 540 processes) 28.9 MB / 1.91%, measuring a 1.47 M-file home 41 MB peak. Bundle 52.9 KB gz (+ uPlot 22.2 KB). Security: no shell, os/exec only in safeexec; all 21 API routes (7 state-changing) refused without key (401), with foreign Host (403) and, when state-changing, with foreign or missing Origin (403), no cookies (cmd/hearthd TestEveryRouteIsGuarded); WS rejects foreign Origin; CSP without unsafe-*; no {@html}; listens on 127.0.0.1 only after install and purge leaves nothing (`make itest`, 31 checks); validator fuzzers 60 s each without findings; app names and unit descriptions with HTML render as text (headless Chromium); govulncheck, npm audit, gosec clean. Not applicable yet: broker, OnCalendar, Docker containers, polkit files.

## Phase 2 — Docker and Homepage (2–3 weeks)
- [x] 2.1 `cmd/hearth-broker`: socket activation, SO_PEERCRED + group check, JSON-RPC, method allowlist, polkit check, idle exit, audit; units + polkit policy in packaging; group `hearth` created in postinstall
- [x] 2.2 Broker Docker proxy: allowlisted read endpoints + container start/stop/restart/pause/remove, image remove/tag/pull, network/volume remove, prune with dry-run size; refusal rules from SECURITY S12
- [x] 2.3 `discovery/docker`: containers, images, networks, volumes, compose projects; `/events` stream
- [x] 2.4 Containers page: list, stats history, actions, live logs, env masking, inspect
- [x] 2.5 Container exec shell in terminal component (xterm lazy-loaded)
- [x] 2.6 Images page with full detail (layers + history, used-by, sizes, config) + "check for update" (registry digest) + optional Trivy
- [x] 2.7 Compose projects page (group actions)
- [x] 2.8 Cleanup view with reclaimable sizes and preview tokens
- [x] 2.9 Homepage tiles: form (name, URL, description, icon, group, linked container, health check), icon set + safe upload, health checker (SECURITY S21)
- [x] 2.10 Homepage suggestions from published ports / local HTTP listeners (pre-filled form, never auto-saved)
- [x] 2.11 Docker resource cards on Home + container alerts (exited unexpectedly)

**Gate 2:** broker tests (unknown method, wrong group, unauthorized destructive, privileged container create, forbidden bind mounts) all rejected; manual pentest of broker by the owner; resource budget still met.

Gate 2 result (2026-10-27): **automated checks passed; owner pentest pending** (owner deferred it and approved starting Phase 3; `phase-2` stays off `main` until it passes). Broker: unknown methods, non-members, unauthorized changes and removes, container create (with Privileged and `/` binds) and path/query injection all refused, and none reached the Engine (unit tests; `make itest`, 89 checks). `test/pentest/brokerprobe`: 198 hostile probes, none through. Two findings from it fixed (2d482f3): params are validated before polkit, so no dialog appears for a request that will be refused, and request lines time out after 5 s, so a member cannot hold all 16 slots. Exec into a privileged container with `/` mounted is refused even when polkit allows it. Broker exposure 1.5, no capabilities, NoNewPrivs, seccomp, no network; audit holds params hashes only. Budget: idle 26.0 MB / 0.11% CPU; Home open (WS client following its topics, no browser available) 28.6 MB / 0.78%; bundle 113 KB gz; hearthd 21.3 MB. Owner's manual checklist: `test/pentest/README.md`.

## Phase 3 — Packages, network, terminal, scheduler (~3 weeks)
- [x] 3.1 `pkgs`: dpkg status parser + fsnotify; PackageKit D-Bus for install/remove/update with simulate; essential/required protection
- [x] 3.2 Flatpak, Snap adapters; pip --user, npm -g, cargo, go bin listing (+ remove where safe)
- [x] 3.3 Packages page (unified + per manager, updates with security flag, hold, files/deps)
- [x] 3.4 `discovery/path`: Tools page (owner package, version, whatis), open in terminal / man ("open in terminal" lands with the Terminal page, 3.7)
- [x] 3.5 `netinfo`: listeners via netlink sock_diag + inode→pid mapping, connections, interfaces, NM state, firewall status (owner decision, 2026-10-28: no broker; other users' sockets are attributed to their unit or container through the socket's cgroup id, exact pids only for the user's own processes, so the broker keeps no capabilities)
- [x] 3.6 Network page + lineage view (port → process → unit → package → container)
- [x] 3.7 Terminal page: multi-tab PTY sessions, "open terminal here" (folders, containers, and the Tools page's "open in terminal")
- [x] 3.8 `scheduler`: OnCalendar visual builder + validator (fuzzed), user timers in `~/.config/systemd/user`, root timers via broker (S13), run now/pause/delete, history from journal, failure alerts
- [x] 3.9 Alert: new listener on 0.0.0.0

**Gate 3:** fuzzing of all validators ≥ 60 s each without findings; e2e XSS and DNS-rebinding tests pass; no `os/exec` outside safeexec.

Gate 3 result (2026-10-29): **passed.** Fuzzing: 30 targets, every validator of input (safeexec, broker Docker refs, procctl targets, package remove targets, settings, tiles and icons, alert rules, job units and schedules, terminal folders, app ids, compose projects), 60 s each; two findings, fixed and fuzzed again clean: procctl accepted a start time with leading zeros (`pid:1000000:000`, harmless, now one canonical form), and the settings validators accepted trailing data after the object (`{}0`, stored as is, which the UI could not parse). e2e (`make itest`, Playwright with Debian's chromium): HTML in a tile, an alert rule, a job name and its output, a container's name, image, status and compose project, a package description and a listening process's name shows as text on every page, and no script or handler ran (7 tests); DNS rebinding: foreign `Host` (4 forms) refused for pages, the API with a valid key and the WebSocket upgrade, and a rebound page's Origin cannot open a terminal. `os/exec` is imported by `internal/safeexec` only. The e2e test found a bug, fixed: a job that exits at once could lose its output in the history (journald may record the line before it can read the process's cgroup, so it has no unit or invocation; reproduced in 3 of 20 runs). `make itest`: 195 checks passed.

## Phase 4 — Games, settings, reports, unique features (~3 weeks)
- [x] 4.1 Games: Steam, Lutris, Heroic, `Categories=Game`; playtime tracking; Games page (launchers keep their own playtime for their games; Hearth samples desktop games once a minute)
- [x] 4.2 Settings page via D-Bus (Wi-Fi/connections, brightness, power profile, audio, time zone, hostname, autostart, power actions) (Wi-Fi passwords never pass through Hearth: NetworkManager asks the desktop's secret agent; audio through pw-dump/wpctl in safeexec; autostart writes only ~/.config/autostart, never deletes)
- [x] 4.3 Reports: machine + item reports, HTML/MD export, print-to-PDF stylesheet, weekly scheduled report (per-item usage sampled from cgroups once a minute, kept hourly for 90 days; exports are one file with a hash-pinned stylesheet and no script; security posture joins the report with 4.4)
- [x] 4.4 Security posture page (listeners on every address, firewall, sshd_config as sshd reads it, security updates, debsecan only on request (S21c), container S12 risks, PATH write access; also in the machine report)
- [x] 4.5 Change timeline + snapshot diff (packages from dpkg's own log; units, containers and listeners from hourly snapshots stored only when something changed, kept 90 days; named snapshots kept until deleted; the audit log joins the timeline (S18) and the machine report)
- [x] 4.6 Profiles (Dev / Gaming auto-mode / Battery) (each step a registered action; restores only what it changed, across restarts; apps are started, never closed; auto mode holds only user units and apps, S15a)
- [x] 4.7 Space advisor (preview + trash) (apt cache and journal through two closed broker methods, S16c; ~/.cache and large files to the XDG trash; old kernels through package removal, never the running one or the two newest; Docker leftovers through the existing cleanup)
- [x] 4.8 "Why is my fan loud?" + per-app energy estimate (causes from temps, GPU, recent load and per-item CPU; energy from hourly CPU time, CPU only, at a rate fitted from this machine's battery hours or a labelled nominal one; RAPL is root's)
- [x] 4.9 Boot analysis + full journal viewer (systemd-analyze time, blame and critical-chain; the journal filtered by unit, priority, time, boot and literal text, newest first with paging; read-only, as the user)
- [x] 4.10 Polish: empty states, compact density, keyboard shortcuts, ar/en review (English ships; every page crawled in normal, compact and right-to-left; check-forbidden refuses physical-direction CSS; notes for an Arabic translation in ARCHITECTURE)

**Gate 4:** full SECURITY.md pre-release checklist signed off; resource budget met → tag v1.0.0 locally (no push without approval).

## Phase 5 — Phone link (3–4 weeks)
- [ ] 5.1 KDE Connect protocol: UDP discovery, TLS identity, pairing with code + fingerprint
- [ ] 5.2 Plugins: ping, battery, clipboard, share (files), notifications, mpris (media), findmyphone
- [ ] 5.3 runcommand with user-defined list only (S23)
- [ ] 5.4 Optional LAN listener + phone PWA (status, alerts, Homepage), per-device permissions
- [ ] 5.5 Devices page: paired devices, revoke, permissions

**Gate 5:** owner pentest of pairing, TLS pinning, revoked device rejection, phone session limits.

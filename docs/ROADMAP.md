# Hearth — Roadmap

Rules: one task at a time, `make check` + commit after each, tick the box in the same commit. A phase ends only when its **gate** passes — report the gate results to the owner and wait for approval before the next phase.

## Phase 0 — Foundations (~1 week)
- [x] 0.1 `git init`, `go.mod` (module `github.com/mohammed-w-mosabeh/hearth`), `.gitignore`, `Makefile` with all targets from CLAUDE.md (stubs allowed), `.editorconfig`
- [x] 0.2 Lint/CI config: `golangci-lint` (incl. `gosec`, `depguard` forbidding `os/exec` outside `internal/safeexec`), custom check script for `sh -c` / `bash -c` / `{@html}`, `govulncheck`, GitHub Actions workflow (not pushed)
- [x] 0.3 `internal/safeexec` with binary allowlist + validators + fuzz tests
- [x] 0.4 `internal/store`: SQLite open (WAL, 0600), migrations runner, `audit_log` table
- [x] 0.5 `internal/auth`: token file (0600, rotate on start, single-use), `/auth?t=` exchange → bearer session key (SECURITY S2; no cookie), middleware
- [ ] 0.6 `internal/api`: server on 127.0.0.1:<port>, Host/Origin middleware, security headers/CSP, rate limit, body limit, `/api/v1/health`, WebSocket hub with Origin check — with tests for every rejection path
- [ ] 0.7 `web/`: Svelte 5 + Vite + TS skeleton, tokens.css (light/dark), sidebar layout, English UI strings in an i18n file (en.json), embedded via `go:embed`
- [ ] 0.8 `cmd/hearthd`: refuses root, loads config, starts server, opens browser on first run (`xdg-open`)
- [ ] 0.9 `packaging/`: hearthd user unit (hardened), nfpm config, desktop file; `make deb` builds an installable package
- [ ] 0.10 `make bench` script: measures RSS/CPU of idle hearthd over 60 s

**Gate 0:** no listener off localhost (`ss -tlnp`), all auth/Origin/Host tests pass, `make check` clean, `.deb` installs and uninstalls cleanly in `make itest` container.

## Phase 1 — MVP: monitor and control (3–4 weeks)
- [ ] 1.1 `collect`: CPU (total, per core, freq, load), RAM/swap — ring buffer + adaptive sampling (1 s with subscribers / 15 s otherwise)
- [ ] 1.2 `store`: downsampler to `metrics_1m` / `metrics_1h` with retention
- [ ] 1.3 `collect`: disks (usage per mount, I/O), network throughput per interface
- [ ] 1.4 `collect`: sensors (hwmon/thermal) and fans; GPU (NVML if present via dlopen-free approach or `nvidia-smi` fallback through safeexec at ≥ 2 s; AMD/Intel sysfs)
- [ ] 1.5 `collect`: battery via UPower D-Bus + power profiles
- [ ] 1.6 WS topics `metrics` + Home page Quick Views cards with uPlot sparklines (draggable grid, layout persisted)
- [ ] 1.7 Resources page: detailed charts with time range (10 min / 24 h / 7 d / 1 y)
- [ ] 1.8 `collect/proc`: processes grouped by app (cgroup/scope → desktop app → exe), CPU/RAM/IO/net
- [ ] 1.9 Per-app actions: terminate, kill (confirm), freeze/resume, renice — with audit log
- [ ] 1.10 `discovery`: `.desktop` apps (+ Flatpak/Snap exports, AppImages) with fsnotify; Applications page (grid, search, favorites)
- [ ] 1.11 App launch via `systemd-run --user --scope` + close action
- [ ] 1.12 `services`: list system + user units via D-Bus with signals; Services page
- [ ] 1.13 Service actions (start/stop/restart/enable/disable) via systemd D-Bus (polkit handles auth); critical-unit double confirm; journal viewer for a unit
- [ ] 1.14 `alerts`: rule engine with duration + hysteresis, default rules, desktop notification channel, Alerts page (rules form + history)
- [ ] 1.15 Command palette Ctrl+K over apps, services, pages
- [ ] 1.16 Disks page: usage, folder size explorer (user-owned paths)

**Gate 1:** `make bench` idle < 40 MB RSS and < 1% CPU; UI open < 60 MB / < 3%; bundle < 300 KB gz; security checklist items applicable so far pass.

## Phase 2 — Docker and Homepage (2–3 weeks)
- [ ] 2.1 `cmd/hearth-broker`: socket activation, SO_PEERCRED + group check, JSON-RPC, method allowlist, polkit check, idle exit, audit; units + polkit policy in packaging; group `hearth` created in postinstall
- [ ] 2.2 Broker Docker proxy: allowlisted read endpoints + container start/stop/restart/pause/remove, image remove/tag/pull, network/volume remove, prune with dry-run size; refusal rules from SECURITY S12
- [ ] 2.3 `discovery/docker`: containers, images, networks, volumes, compose projects; `/events` stream
- [ ] 2.4 Containers page: list, stats history, actions, live logs, env masking, inspect
- [ ] 2.5 Container exec shell in terminal component (xterm lazy-loaded)
- [ ] 2.6 Images page with full detail (layers + history, used-by, sizes, config) + "check for update" (registry digest) + optional Trivy
- [ ] 2.7 Compose projects page (group actions)
- [ ] 2.8 Cleanup view with reclaimable sizes and preview tokens
- [ ] 2.9 Homepage tiles: form (name, URL, description, icon, group, linked container, health check), icon set + safe upload, health checker (SECURITY S21)
- [ ] 2.10 Homepage suggestions from published ports / local HTTP listeners (pre-filled form, never auto-saved)
- [ ] 2.11 Docker resource cards on Home + container alerts (exited unexpectedly)

**Gate 2:** broker tests (unknown method, wrong group, unauthorized destructive, privileged container create, forbidden bind mounts) all rejected; manual pentest of broker by the owner; resource budget still met.

## Phase 3 — Packages, network, terminal, scheduler (~3 weeks)
- [ ] 3.1 `pkgs`: dpkg status parser + fsnotify; PackageKit D-Bus for install/remove/update with simulate; essential/required protection
- [ ] 3.2 Flatpak, Snap adapters; pip --user, npm -g, cargo, go bin listing (+ remove where safe)
- [ ] 3.3 Packages page (unified + per manager, updates with security flag, hold, files/deps)
- [ ] 3.4 `discovery/path`: Tools page (owner package, version, whatis), open in terminal / man
- [ ] 3.5 `netinfo`: listeners via netlink sock_diag + inode→pid mapping (broker for other users' processes), connections, interfaces, NM state, firewall status
- [ ] 3.6 Network page + lineage view (port → process → unit → package → container)
- [ ] 3.7 Terminal page: multi-tab PTY sessions, "open terminal here"
- [ ] 3.8 `scheduler`: OnCalendar visual builder + validator (fuzzed), user timers in `~/.config/systemd/user`, root timers via broker (S13), run now/pause/delete, history from journal, failure alerts
- [ ] 3.9 Alert: new listener on 0.0.0.0

**Gate 3:** fuzzing of all validators ≥ 60 s each without findings; e2e XSS and DNS-rebinding tests pass; no `os/exec` outside safeexec.

## Phase 4 — Games, settings, reports, unique features (~3 weeks)
- [ ] 4.1 Games: Steam, Lutris, Heroic, `Categories=Game`; playtime tracking; Games page
- [ ] 4.2 Settings page via D-Bus (Wi-Fi/connections, brightness, power profile, audio, time zone, hostname, autostart, power actions)
- [ ] 4.3 Reports: machine + item reports, HTML/MD export, print-to-PDF stylesheet, weekly scheduled report
- [ ] 4.4 Security posture page
- [ ] 4.5 Change timeline + snapshot diff
- [ ] 4.6 Profiles (Dev / Gaming auto-mode / Battery)
- [ ] 4.7 Space advisor (preview + trash)
- [ ] 4.8 "Why is my fan loud?" + per-app energy estimate
- [ ] 4.9 Boot analysis + full journal viewer
- [ ] 4.10 Polish: empty states, compact density, keyboard shortcuts, ar/en review

**Gate 4:** full SECURITY.md pre-release checklist signed off; resource budget met → tag v1.0.0 locally (no push without approval).

## Phase 5 — Phone link (3–4 weeks)
- [ ] 5.1 KDE Connect protocol: UDP discovery, TLS identity, pairing with code + fingerprint
- [ ] 5.2 Plugins: ping, battery, clipboard, share (files), notifications, mpris (media), findmyphone
- [ ] 5.3 runcommand with user-defined list only (S23)
- [ ] 5.4 Optional LAN listener + phone PWA (status, alerts, Homepage), per-device permissions
- [ ] 5.5 Devices page: paired devices, revoke, permissions

**Gate 5:** owner pentest of pairing, TLS pinning, revoked device rejection, phone session limits.

# Hearth — Architecture

## Processes

```
Browser (PWA) ──127.0.0.1 + session token──┐
Phone (opt-in, LAN, TLS + paired key) ─────┤
                                           ▼
              hearthd  (user service, systemd --user, runs as the logged-in user)
              ├─ api/        HTTP + WebSocket, auth, CSRF/Origin/Host checks
              ├─ collect/    metrics samplers (cpu, mem, disk, gpu, sensors, net, battery)
              ├─ discovery/  apps, tools, packages, games, docker, services, ports
              ├─ actions/    typed actions with risk levels
              ├─ alerts/  scheduler/  reports/  terminal/  phone/
              └─ store/      SQLite (WAL) + in-memory ring buffers
- - - - - - - - - - - privilege boundary - - - - - - - - - - -
   kernel (/proc, /sys, hwmon, NVML)   D-Bus system services          hearth-broker (root)
   read-only, no root                  systemd, PackageKit, NM,       socket-activated, exits
                                       UPower, UDisks2, logind,       after 60 s idle; closed
                                       timedated, hostnamed           allowlist: Docker API
                                       → polkit prompts               subset, smartctl, root
                                                                      timers, ss -p for all users
```

- **hearthd**: `/usr/bin/hearthd`, unit `/usr/lib/systemd/user/hearthd.service`. Never root (refuses to start if `euid == 0`).
- **hearth-broker**: `/usr/bin/hearth-broker`, `hearth-broker.socket` at `/run/hearth/broker.sock` mode `0660`, group `hearth`. Speaks a small JSON-RPC over the Unix socket. Checks the caller with `SO_PEERCRED` (uid must be in group `hearth`). Every method is in a static allowlist; destructive methods call polkit `CheckAuthorization` for the caller before acting. No "exec arbitrary command" method exists; the one interactive method, `docker.containers.exec`, runs a fixed `/bin/sh` under SECURITY S12a. Methods are calls (one answer), streams (items until the end) or sessions (items both ways, e.g. terminal input and resize).

## Directory layout

```
hearth/
  CLAUDE.md
  Makefile
  go.mod
  cmd/
    hearthd/main.go
    hearth-broker/main.go
  internal/
    api/          # router, middleware (auth, origin, host, csp, ratelimit), ws hub
    auth/         # launch token file, bearer session keys, pairing (phone)
    store/        # sqlite open/migrate, ringbuf, downsampler, audit log
    model/        # Item, Action, Risk, Metric types shared by modules
    collect/      # cpu.go mem.go disk.go gpu_nvml.go gpu_sysfs.go sensors.go net.go battery.go proc.go
    discovery/    # desktop.go appimage.go path.go dpkg.go flatpak.go snap.go pip.go npm.go docker.go games_steam.go games_lutris.go games_heroic.go systemd.go ports.go
    actions/      # registry, risk, preview/simulate, undo
    docker/       # client wrapper via broker
    pkgs/         # PackageKit + per-manager adapters
    services/     # systemd D-Bus wrapper
    launch/       # app launch: Exec= parsing, transient app-hearth-<id>@<n>.service via the user manager (D-Bus)
    netinfo/      # sock_diag, interfaces, NM
    alerts/       # rules, evaluator, hysteresis, channels
    scheduler/    # systemd timer generator, history from journal
    terminal/     # pty sessions
    reports/      # html/md renderers
    phone/        # kdeconnect protocol (phase 5)
    broker/       # client (hearthd side) + server (broker side) + allowlist
    dbusx/        # small helpers over godbus
    safeexec/     # the ONLY place allowed to call os/exec; validates args
  web/
    index.html
    src/
      app.ts  routes/  lib/api.ts  lib/ws.ts  lib/i18n/en.json
      components/  styles/tokens.css
    vite.config.ts
  packaging/
    nfpm.yaml
    systemd/hearthd.service  hearth-broker.socket  hearth-broker.service
    polkit/org.hearth.policy
    apparmor/usr.bin.hearthd  usr.bin.hearth-broker
    desktop/hearth.desktop
    scripts/postinstall.sh  preremove.sh  postremove.sh
  test/
    itest/        # Debian 13 container with systemd + docker, Go integration tests
    e2e/          # Playwright
  docs/
```

## Conventions

- **Go**: Go 1.22+ `net/http` routing (`mux.HandleFunc("GET /api/v1/containers", ...)`). Context everywhere, timeouts on every external call (D-Bus 5 s, exec 10 s default). Errors wrapped with `%w`. `log/slog` JSON logs, never log secrets.
- **os/exec only inside `internal/safeexec`**. `safeexec.Run(ctx, bin, args...)` resolves `bin` against an allowlist of absolute paths, validates each arg with a per-command validator, never uses a shell. A lint rule forbids importing `os/exec` elsewhere.
- **API**: REST under `/api/v1`, JSON. Live data over one WebSocket `/api/v1/ws` with topic subscriptions (`metrics`, `procs`, `docker` (snapshots + events), `alerts`, `logs:<id>`, `term:<id>`). Server sends only deltas.
- **Actions endpoint**: `POST /api/v1/actions/{kind}/{action}` body `{ "target": "...", "params": {...}, "preview": true|false, "confirm": "<name>" }`. `preview: true` returns what would happen without doing it. Destructive actions without a prior preview token are rejected.
- **Database**: migrations in `internal/store/migrations/NNNN_*.sql`, applied at start. Tables: `items_override`, `homepage_tiles`, `alert_rules`, `alert_events`, `metrics_1m`, `metrics_1h`, `audit_log`, `jobs`, `profiles`, `devices` (phone), `settings`.
- **Sampling**: 1 s while a UI client subscribes to live metrics; 15 s otherwise (alerts only). Process sampling only while the processes page is open (or for alert rules that need it).
- **Discovery**: full scan at start, then incremental via fsnotify / D-Bus signals / Docker `/events`. Debounce bursts (500 ms).
- **Frontend**: Svelte 5 runes, TypeScript strict. No `{@html}`. All data rendered as text. Route-level code splitting; xterm.js and uPlot lazy-loaded. CSS tokens in `styles/tokens.css`, light/dark via `prefers-color-scheme`.
- **i18n**: English only (`en.json`); structure allows adding more locales later. Numbers use tabular figures.

## Design direction
- Calm dashboard, not a crowded admin panel. One accent color (warm amber), status colors only for status.
- Fonts: Inter or IBM Plex Sans (UI), JetBrains Mono (numbers, terminal), bundled locally (no CDN).
- Narrow icon sidebar + wide content; draggable card grid on Home.
- Motion ≤ 150 ms, respect `prefers-reduced-motion`. Comfortable / compact density toggle.
- Keyboard first: Ctrl+K palette, `g d` → Docker, etc.
- Empty states explain and offer the next step (e.g. "Docker is not installed").
- Reversible actions: no dialog + Undo toast. Destructive: dialog, type-to-confirm for high impact.

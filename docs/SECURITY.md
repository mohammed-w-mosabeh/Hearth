# Hearth — Security (NON-NEGOTIABLE)

Hearth is effectively a root-capable control panel. Assume any web page the user visits, any device on the same network, and any string coming from the system (container names, labels, package descriptions, file names) can be hostile.

## Assets
1. Root on the machine (including Docker socket = root).
2. User files, secrets in container env vars, Hearth DB.
3. System stability (no accidental removal of critical packages/services).

## Invariants

### Network & web
- S1. Bind `127.0.0.1` only by default. Port chosen at first run, stored in config. Phone/LAN listener is a separate listener, disabled by default.
- S2. Launch token: 32 random bytes from `crypto/rand`, stored in `$XDG_CONFIG_HOME/hearth/token` mode `0600`, rotated on each hearthd start and **single-use**: a successful exchange burns it and writes a fresh one. The launcher opens `http://127.0.0.1:<port>/auth?t=<token>`; the server redirects (`303`, `Cache-Control: no-store`) to `/#k=<session key>` (fragment, never sent to a server). The UI keeps the key in `localStorage` (isolated per origin incl. port), removes it from the URL, and sends `Authorization: Bearer <key>` on API calls and `Sec-WebSocket-Protocol: hearth.v1, hearth.key.<key>` on WebSocket upgrades (server echoes only `hearth.v1`). **No cookies**: browsers send cookies to every port on the same host, so any other local web service (e.g. a container on `127.0.0.1:8080`) would receive them. Sessions live in memory as SHA-256 hashes, at most 16, and all end when hearthd restarts.
- S3. On every request: `Host` must be exactly `127.0.0.1:<port>` or `localhost:<port>` (DNS rebinding). On every non-GET request and every WebSocket upgrade: `Origin` must match. Reject otherwise with 403.
- S4. Headers: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-origin`.
- S5. Rate limit state-changing endpoints (e.g. 20/s burst 40). Request body limit 1 MiB (uploads 5 MiB, icons only, sniffed as PNG/SVG-sanitized/WebP).
- S6. No `{@html}` in Svelte, no `innerHTML`. Uploaded SVG icons are rasterized or sanitized server-side; served with `Content-Type: image/svg+xml` + CSP sandbox.

### Execution
- S7. No shell, ever. No `sh -c`, `bash -c`, `system()`. `os/exec` only in `internal/safeexec` with absolute-path allowlist and per-argument validators. CI lint fails otherwise.
- S8. Validators: Debian package name `^[a-z0-9][a-z0-9+.-]+$`; systemd unit name per `systemd.unit(5)`; container/image IDs `^[a-f0-9]{12,64}$` or validated refs; paths cleaned, absolute, and checked against allowed roots. Arguments starting with `-` are rejected where a name is expected.
- S9. Prefer D-Bus APIs over CLIs (systemd, PackageKit, NetworkManager, UPower, UDisks2, logind, timedated, hostnamed).

### Privilege
- S10. `hearthd` refuses to run as root.
- S11. `hearth-broker`: socket-activated, exits after 60 s idle; verifies caller via `SO_PEERCRED`; static method allowlist; destructive methods require polkit authorization of the calling uid, asked only after the call's params are validated (no password dialog for a request that would be refused); a request line must arrive within 5 s and at most 16 connections are served at once; every call written to the audit log; hardened unit (see below).
- S12. Docker via broker only, and only an API subset. Refuse from the UI: creating containers with `Privileged`, `CapAdd`, host PID/network/IPC namespaces, or bind mounts of `/`, `/etc`, `/root`, `/var/run/docker.sock`, `/proc`, `/sys`, `/dev`. (Viewing such existing containers is fine and flagged in Security posture.)
- S12a. Container exec shell (owner decision, 2026-10-27): broker method `docker.containers.exec` only. Fixed command `/bin/sh` (no caller-chosen command, user or env); running containers only; refused for any container with an S12 risk (privileged, added capabilities, host PID/network/IPC, risky bind mounts or the Docker socket), since a shell there is a host root shell; polkit `org.hearth.docker.exec` is `auth_admin` with no keep, so every session asks for the password; at most 2 sessions at once; start and end audited by the broker and by hearthd. Never offered to phone sessions.
- S13. Root scheduled jobs: script copied to `/var/lib/hearth/jobs/<id>/run` owned by root mode `0755`; never executed from a user-writable path. Requires polkit.
- S14. Terminal runs as the user: hearthd opens the pty and passes only its fd to a transient unit of the user's systemd manager running the user's login shell (from passwd, if `/etc/shells` lists it); hearthd executes nothing, at most 8 shells at once, each opening audited. Never a root shell on the host from the UI (container shells: S12a only). Disabled for phone sessions unless re-enabled with password re-entry.

### Destructive actions
- S15. Preview/simulate first (apt simulate, Docker reclaimable size, file list), return a short-lived preview token; execute only with that token.
- S15a. Profiles (SPEC 18) run each step as its registered action, audited; a step that would need a typed confirmation (critical units, hearthd's own unit) is refused when the profile is saved and again when it runs, and apps are started but never closed (closing can lose unsaved work). Activating by hand previews what will change, with a token when anything stops. A profile may run unattended while a game runs only if the user turned that on for it, and then holds only user units and apps: a system unit or container may open a password dialog (`org.hearth.docker.control` is `auth_admin_keep`), which must not appear over a game or come to be expected without a click. Games are found in the command lines of the user's own processes, never their environment, and nothing of those is stored. Each automatic change is audited and shown in a desktop notification.
- S16. Block removal of `Essential: yes` / `Protected: yes` / `Priority: required` packages and of Hearth itself, also when another change would take one along. Double confirm for critical units.
- S16a. Packages outside dpkg: snaps are removed through snapd (polkit); Flatpak, npm (user-owned prefix only), cargo and Go binaries by the manager's own uninstall (`gio trash` for a Go binary) run as a transient unit of the user's systemd manager, as the user and outside hearthd's sandbox, with a fixed argv built from the listing (validated names, no shell, no environment expansion). pip `--user` packages are listed, never removed (it would need `--break-system-packages`). Runtimes in use (and their extensions: GL drivers, codecs, Compat), system snaps, npm itself and root-owned prefixes are blocked.
- S16b. Holding a Debian package (PackageKit has no hold) is the broker's `pkg.hold`: polkit `org.hearth.packages.hold` (`auth_admin_keep`), then `apt-mark hold|unhold <name[:arch]>` through safeexec with the name validated before polkit is asked. The broker unit may write `/var/lib/dpkg` for it and still has no capabilities.
- S17. Files go to the XDG trash (`gio trash` semantics) instead of unlinking, where applicable.
- S18. Audit log: append-only table (time, origin, uid, action, target, params hash, result). Viewable in the Change timeline.

### Secrets & data
- S19. Mask env values whose key matches `(?i)(pass|pwd|token|secret|key|auth|cred)`; reveal only on explicit click, never in logs, reports or exports.
- S20. DB, config and token files mode `0600`, dirs `0700`.
- S21. Health checks (Homepage): only user-entered URLs, `GET/HEAD`, 5 s timeout, max 1 redirect to the same host, no credentials unless the user configured them, response body discarded after 64 KiB.
- S21a. Image update check (Docker images): only when the user asks; HTTPS only; anonymous pull tokens from the registry's Bearer challenge (HTTPS realm only), never the user's Docker credentials; manifest HEAD/GET only, 10 s timeout, redirects only within the host, bodies bounded (manifest 4 MiB, token 64 KiB).
- S21b. Homepage suggestions: only when the user asks; only this machine's own listeners (from `/proc/net/tcp*`) and published container ports, never hearthd's own port or known non-HTTP ports, at most 64 probes, 8 at a time; one `GET /` each, 1.5 s timeout, no redirects followed, no proxy, credentials or cookies, body discarded after 64 KiB; the page title is plain text (escaped in the UI). Nothing is saved without the user saving the form.
- S21c. Known CVEs (Security posture): `debsecan`, only if installed and only when the user asks; one fixed argv through safeexec (`--suite <codename from /etc/os-release> --format summary`), 2 min timeout, output bounded (1 MiB). debsecan fetches the Debian security tracker's public data over HTTPS; nothing about the machine is sent but the release name in the URL. The run is audited; its result is kept in the settings table.

### Phone link (phase 5)
- S22. Off by default. When enabled: TLS with a self-signed cert generated on first enable, pinned by paired devices; pairing via one-time 6-digit code shown on the desktop + fingerprint confirmation; per-device keys revocable; phone sessions have no terminal and no destructive actions unless the user grants them per device.
- S23. KDE Connect "run command" plugin only runs commands from a user-defined list, by ID, never a string sent by the phone.

### Supply chain & build
- S24. Pinned deps (`go.sum`, `package-lock.json`), `govulncheck` + `npm audit --audit-level=high` in CI, `gosec`, minimal deps (justify every new one).
- S25. Reproducible build flags (`-trimpath -buildvcs=true`), signed `.deb`, checksums published.

### systemd hardening (both units, adjust per need)
```
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only        # hearthd: ReadWritePaths=%h/.local/share/hearth %h/.config/hearth %h/.config/systemd/user %h/.config/autostart
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes     # broker only if not needed
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictRealtime=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
MemoryMax=80M
CPUQuota=10%
```
Note: some hearthd features (launching apps, PTY, reading other users' /proc) need relaxations; document each relaxation in the unit file with a comment.

## Pre-release checklist (must all pass for a phase gate that says "security")
- [ ] `grep -rn 'sh -c\|bash -c' --include=*.go` returns nothing; `os/exec` imported only in `internal/safeexec`
- [ ] Every state-changing route covered by a test that fails without session key / with bad Origin / bad Host
- [ ] No `Set-Cookie` on any response; session key accepted only via `Authorization` or WS subprotocol
- [ ] WebSocket upgrade rejects foreign Origin
- [ ] CSP has no `unsafe-inline` / `unsafe-eval`; no `{@html}` (lint)
- [ ] `ss -tlnp` after install shows Hearth only on 127.0.0.1
- [ ] Broker rejects unknown methods, non-group callers, and unauthorized destructive calls (tests)
- [ ] Fuzz tests for validators (package names, unit names, paths, OnCalendar, URLs) run ≥ 60 s each without findings
- [ ] XSS e2e: container named `<img src=x onerror=alert(1)>` renders as text
- [ ] DNS rebinding test: request with `Host: evil.test` → 403
- [ ] `govulncheck`, `npm audit`, `gosec` clean (no high)
- [ ] `apt purge hearth` leaves no units, sockets, polkit files, group (if empty) or `/var/lib/hearth`

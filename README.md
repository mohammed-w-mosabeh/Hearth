# Hearth

A lightweight, local-first system manager and monitor for a single Debian desktop or laptop. One place for what Homepage, Portainer, btop, Cockpit, a cron UI and the GUI package manager each do in part: resources, disks and battery, services, Docker, packages, applications, tools, network and ports, scheduled jobs, alerts, security posture and a terminal.

![Home, before and after the Ember redesign](docs/design/screens/home.dark.comfortable.ltr.jpg)

## How it works
- `hearthd` runs as you, as a systemd user service, and serves the UI on `127.0.0.1` only. There is no LAN listener.
- Anything that needs root goes through polkit or `hearth-broker`, a small closed allowlist, never a shell.
- Destructive actions show a preview first, ask for confirmation and are written to an audit log.
- Everything is discovered on its own. Settings, history and audit live in one SQLite file at `$XDG_DATA_HOME/hearth/hearth.db`.

The full rules are in [`docs/SECURITY-INVARIANTS.md`](docs/SECURITY-INVARIANTS.md).

## The interface
The Ember design ([`docs/design/ember.md`](docs/design/ember.md)): dark by default, plus light or the system's choice; comfortable or compact density; English and Arabic, right to left. Its parts:
- a grouped rail
- a pulse strip with CPU, memory, temperature, network and battery on every page
- a health ring and a Needs attention queue on Home
- an inspector drawer beside every list
- Ctrl+K for every page, app and service, and `g` shortcuts (`?` lists them)

Every page is checked for WCAG AA contrast and with axe. More screenshots are in [`docs/design/screens/`](docs/design/screens/).

## Install
Build the package and install it:

```sh
make deb
./scripts/install-local.sh   # asks for sudo; restarts hearthd
```

Open Hearth from the application menu, or run `hearthd open`. The local CLI is `hearth` (`hearth status`, `hearth ports`, `hearth alerts`).

## Develop
Go and Svelte 5 + Vite + TypeScript. The UI is embedded in the binary.

| Command | What it does |
|---|---|
| `make dev` | hearthd with the Vite dev server, still on 127.0.0.1 |
| `make build` | the web bundle and the binaries |
| `make check` | lint, tests and vulnerability scan |
| `make itest` | integration and browser tests in a Debian 13 container with systemd and Docker |
| `make bench` | the resource budget |
| `make deb` | the `.deb` |

Read [`docs/SPEC.md`](docs/SPEC.md), [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), [`docs/SECURITY-INVARIANTS.md`](docs/SECURITY-INVARIANTS.md) and [`docs/ROADMAP.md`](docs/ROADMAP.md) before changing anything.

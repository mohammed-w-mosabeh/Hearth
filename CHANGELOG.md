# Changelog

Every release of Hearth, newest first. Versions follow the git tags. The detail of each change is in [`docs/ROADMAP.md`](docs/ROADMAP.md), phase by phase.

## Unreleased
- Terminal: shells start again under the packaged sandbox on a real host, and closing a tab ends an idle shell.

## 1.9.0 — 2026-11-14
- First public release under Apache-2.0: licence and third-party notices, security policy, contributing guide, reproducible release build with `SHA256SUMS`.
- `hearthd` turns transparent huge pages off for itself, which keeps idle memory under the 40 MB budget.

## 1.8.0 — 2026-11-11
Automations, and the two phases before it, which were merged without a release of their own.

- **Automations** (Phase 12): one rules engine, "when a trigger fires, if conditions hold, then run steps". 21 trigger kinds, approvals for privileged and destructive steps, protected targets, cooldowns and a global limit that pauses everything, a dry run over the last 7 days, Test now, a gallery of templates. Alert rules and mode triggers became automations.
- **Uninstall completely** (Phase 11, planned as 1.7): removes a program with its newly orphaned dependencies, running parts and leftovers, for apt, Flatpak, Snap, AppImage and manual installs, with a preview and a restore within 7 days.
- **Modes** (Phase 10, planned as 1.6): a named set of changes switched on and off together, with a snapshot to revert to, triggers, approvals and unattended grants. Replaces Profiles.

## 1.5.0 — 2026-11-08
- **Apps**: stacks deployed from a validated spec only, with a canonical compose file, path checks, secrets, danger flags behind a separate polkit action, an import of existing Compose projects with a dry run, backups, scheduled backups and restore, and an optional `<app>.localhost` reverse proxy on 127.0.0.1.

## 1.4.0 — 2026-11-04
- **Ember redesign**: new tokens and typography, a grouped rail, the pulse strip, a Home page with a health ring and a Needs attention queue, an inspector drawer beside every list, motion and empty states, WCAG AA checks. The old UI was removed.

## 1.3.0 — 2026-11-02
- **Security and automation**: Lynis audits, a firewall (ufw) page, integrity watch, access review, a local API with the `hearth` CLI, and Compose updates.

## 1.2.0 — 2026-11-01
- **Insight**: forecasts, event markers on every chart, crashes and OOM kills, sleep and power history, connectivity checks, a log explorer, a hardware inventory and Homepage checks.
- Also the work planned as 1.1: unit editing and resource caps, SMART, more alerts, Docker networks and volumes, "restart needed", unattended-upgrades status, a backup of Hearth's own settings and the Arabic locale.

## 1.0.0 — 2026-10-31
- First complete version: live monitoring with history, applications, services, processes, Docker through the root broker, Homepage tiles, packages, network and ports, a terminal, scheduled jobs, alerts, games, settings, reports, the security posture page, a change timeline, the space advisor and boot analysis.

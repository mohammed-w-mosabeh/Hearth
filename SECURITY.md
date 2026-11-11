# Security policy

Hearth manages a machine: it can start and stop services, install and remove packages and run containers. Security reports are welcome and are handled before anything else.

## Reporting a vulnerability

Report privately through GitHub: on the repository page open **Security → Report a vulnerability** ([private vulnerability reporting](https://github.com/mohammed-w-mosabeh/Hearth/security/advisories/new)).

Please do not open a public issue for a vulnerability, and do not send it by email; there is no security mailbox.

A useful report says:
- the Hearth version (`hearthd --version`) and the Debian release;
- what an attacker needs (a web page the user visits, a local user, a member of the `hearth` group, a hostile container or package name);
- the steps, and what happens.

You will get an answer in the advisory thread. This is a one-maintainer project, so allow about a week for a first reply. A fix is released before the advisory is published, and the reporter is credited unless they ask not to be.

## Supported versions

Only the latest release gets security fixes.

| Version | Supported |
|---|---|
| latest release (1.8.x) | yes |
| anything older | no; upgrade first |

## Scope

In scope:
- reaching the UI or API from another origin, another host or another local user (DNS rebinding, CSRF, a missing token, `Origin` or `Host` check);
- running anything as root, or anything outside the closed allowlists, through `hearthd`, `hearth-broker` or the packaged systemd units;
- script injection through a string that comes from the system (container, unit, package, file or network names);
- a secret shown or written where it should be masked;
- a destructive action that runs without its preview, confirmation or audit entry.

Out of scope:
- what a user who already has root, or the session's own user, can do without Hearth;
- members of the `docker` group, who are root on the machine with or without Hearth;
- findings that need a modified binary or a changed polkit configuration.

## The security model

The rules Hearth is built to are in [`docs/SECURITY-INVARIANTS.md`](docs/SECURITY-INVARIANTS.md). In short: the UI listens on `127.0.0.1` only; every state-changing request needs the session token and passes `Origin` and `Host` checks; `hearthd` never runs as root, and root work goes through polkit or the `hearth-broker` allowlist; nothing is ever run through a shell; there is no telemetry and no account.

Packages are not signed yet. Until they are, check a downloaded `.deb` against the release's `SHA256SUMS`, or build from source.

# QA report — first public release

ROADMAP R.9. Every result below was observed on 2026-11-12 on the tree of
commit `d010312` plus the changes of this commit (two more accessibility
passes and their check line). Nothing is carried over from earlier runs.
The host was a Debian 13 amd64 desktop; the container runs use the
throwaway Debian 13 + systemd image of `make itest`, with nothing of the
host mounted.

## Summary

| Run | Result |
| --- | --- |
| `make check` (lint, tests, vulnerability scan) | passed |
| `make itest` (package in a Debian 13 container) | passed, 761 checks |
| `test/release/run.sh` (upgrade, restarts, lost services, full disk) | passed, 39 checks |
| `make bench` (idle budgets) | passed |
| Frontend bundle and binary size | inside the budget, measured by hand |

No run failed in this task; one flaky test found afterwards is described
under `make check`. What these runs do not
cover is listed at the end.

## `make check`

- Lint: gofmt, the forbidden-pattern rule (no `sh -c`, no `{@html}`), the
  private-pattern guard, the third-party notices check,
  `desktop-file-validate`, `go vet`, `golangci-lint`, `gosec`,
  `svelte-check` and `eslint`: clean.
- Go tests: 63 packages, all `ok`.
  Found afterwards, while repeating `make check` on the cleaned history:
  `TestOtherFolderCancelsJob` in `internal/diskusage` failed once. It was
  a race in the test, not in the scanner (the scan it meant to cancel could
  finish first; about 1 run in 400 under stress, in either tree). The test
  now holds the scan open until the cancelling request was made, and
  passed 1,000 repeats and 20 under the race detector.
- Frontend tests (vitest): 60 files, 307 tests, all passed.
- `govulncheck ./...`: no vulnerabilities found.
- `npm audit --audit-level=high`: passes. It reports 12 findings below that
  level (2 low, 10 moderate), all in the lint toolchain (`eslint`,
  `@typescript-eslint`, `minimatch`), which is used at development time
  and is not part of the bundle that ships.

## `make itest`

The package is installed, used and removed in the container through the
real daemon, systemd, polkit and PackageKit.

- 761 checks passed, none failed.
- Broker pentest probes: 991 hostile requests refused as expected.
- Browser tests (Playwright with Debian's chromium), all passed:
  - every page shows hostile strings as text and nothing runs (XSS);
  - axe finds no serious or critical violation on any of the 26 pages, in
    the dark and the light theme, left to right and right to left;
  - new in this task: the same axe pass at the compact density, once dark
    and left to right and once light and right to left;
  - focus rings, the palette, the shortcuts and a confirmation work from
    the keyboard alone; a managed app's drawer passes both suites;
  - DNS rebinding is refused.
- Removing the package leaves no runtime folder, broker or group behind.

## `test/release/run.sh`

What `make itest` does not do, in the same image. The old package was
built from the oldest tag, `v1.0.0`; the new one is `1.8.0+git14.d010312`.

- **Upgrade from 1.0.0:** the new hearthd starts on the old database; a
  setting saved by the old version is still there; the port is the same;
  the database is still mode 0600; no migration error in the journal.
- **Restart with a session open:** healthy again within 20 s; the old
  session key never gets a 5xx; a new key works. After `kill -9`, systemd
  starts hearthd again.
- **The Docker engine goes away mid-session:** the API keeps answering
  without a server error and hearthd stays healthy.
- **polkit is not running:** a privileged action answers without a crash;
  services, packages, network, security, Docker and alerts all answer
  without a server error.
- **The disk fills while hearthd runs** (an 8 MB tmpfs over the data
  folder): hearthd stays up, reads answer, a write is refused with a 500
  and is not accepted; once space is back the write succeeds.
- **A start on a full disk:** hearthd does not start, and the journal says
  why (`store: ping …/hearth.db: disk I/O error`). With space back it
  starts on the kept data and the setting is still there. Whether
  hearthd should instead serve read-only on a full disk is left for the
  owner to judge; the checks accept either.

## `make bench`

| Measurement over 60 s, UI closed | RSS peak | RSS average | CPU average | Budget |
| --- | --- | --- | --- | --- |
| Idle | 36.7 MB | 36.2 MB | 0.25 % | < 40 MB, < 1 % |
| Idle, 20 automations on | 37.8 MB | 36.8 MB | 0.38 % | < 40 MB, < 1 % |

One round of each. The headroom on RSS is small (2.2 MB with 20
automations on).

## Sizes

Measured by hand on the build the runs above used; no make target
enforces these two yet.

- Binaries: `hearthd` 21.9 MiB, `hearth-broker` 8.5 MiB, `hearth`
  6.4 MiB; budget < 30 MB each.
- Frontend, `gzip -9`: the page, its script and styles and the chart
  library come to 276 kB (269 KiB); budget < 300 KB. The terminal
  (xterm.js) is loaded only when opened and is outside the budget by
  definition. The Arabic strings are a separate file of 44 kB, loaded only
  when Arabic is chosen; with it the total is 320 kB, so an Arabic
  session is over the budget if that file is counted.

## Not covered

These were not tested in this task and are not claimed as passing.

- **The budget with the UI open** (< 60 MB RSS, < 3 % CPU): `make bench`
  measures only the idle case.
- **A real desktop session:** the container has no graphical session, so
  notifications on a real desktop, the polkit password dialog as a user
  sees it, the idle monitor, displays, Wi-Fi and VPN switches, real
  hardware sensors, a battery and a GPU are exercised only through
  simulated sources or not at all.
- **A real Docker engine:** the container uses a fake engine; real
  containers, images and managed apps were not exercised here.
- **Visual checks by eye:** layout, the Arabic right-to-left rendering
  and the compact density were checked by axe only, not looked at.
- **Screen readers:** axe and keyboard tests are no substitute for a pass
  with a real screen reader.
- **Browsers other than chromium.**
- **Upgrades from the tags between 1.0.0 and 1.8.0:** only the oldest
  tag was tried.
- **Other distributions and architectures:** Debian 13 on amd64 only.
- **The release gate itself:** the cleaned history, and the README and
  CONTRIBUTING commands on a clean container, belong to R.10 and the gate.

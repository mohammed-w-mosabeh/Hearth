# Contributing to Hearth

Thank you for looking. Hearth is a one-maintainer project with strict rules, because it can change the machine it runs on. Reading this first saves us both time.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Contributions are accepted under the project's licence, [Apache-2.0](LICENSE).

## Before you write code
- **A vulnerability** is never an issue or a pull request: see [`SECURITY.md`](SECURITY.md).
- **A bug**: open an issue with the version (`hearthd --version`), the Debian release and the steps.
- **A feature**: open an issue first. Hearth is for one Debian machine and one user; remote access, a LAN listener, other distributions and plugin systems are out of scope.
- Read [`docs/SECURITY-INVARIANTS.md`](docs/SECURITY-INVARIANTS.md). A change that conflicts with it is not merged, whatever it adds.

## Set up
You need Debian 13 (or close), Go 1.27.4, Node.js 22 with npm, `make`, `git`, `jq` and `desktop-file-utils`. Docker is needed only for `make itest`.

```sh
git clone https://github.com/mohammed-w-mosabeh/Hearth.git
cd Hearth
make tools    # golangci-lint, govulncheck, gosec and nfpm, pinned, into .tools/bin
make build    # the UI bundle and the three binaries in bin/
make check    # lint, tests and vulnerability scan: must pass before every commit
```

| Command | What it does |
|---|---|
| `make dev` | hearthd with the Vite dev server, on 127.0.0.1 |
| `make test` | `go test ./...` and `vitest` |
| `make lint` | gofmt, golangci-lint, gosec, svelte-check, eslint and the forbidden-pattern check |
| `make vuln` | govulncheck and `npm audit` |
| `make itest` | installs the `.deb` in a Debian 13 container with systemd and Docker and runs the integration and browser tests |
| `make bench` | measures idle memory and CPU against the budget |
| `make deb` | builds `dist/hearth_<version>_amd64.deb` |
| `make notices` | rewrites `THIRD_PARTY_NOTICES.md` after a dependency change |

Never try a destructive action on your own machine while developing (removing packages, pruning Docker, stopping real services). That is what `make itest` and its container are for.

## Rules for a change
1. **No shell.** Never `sh -c`. Use D-Bus, or `internal/safeexec` with a fixed binary and validated arguments. New commands are added to its allowlist with a validator and a fuzz test.
2. **No root in `hearthd`.** Root work goes through polkit or a new, narrow `hearth-broker` method.
3. **Destructive actions** have a preview, a confirmation and an audit entry.
4. **System strings are data.** Names of containers, units, packages and files are shown as text: no `{@html}`, no `innerHTML`. The CSP stays strict.
5. **Secrets are masked** in logs, the UI and reports.
6. **Dependencies stay few.** A new Go module or npm package needs a one-line justification in the commit body, a pinned version, and `make notices`.
7. **Budgets hold**: idle `hearthd` under 40 MB and 1 % CPU, the UI bundle under 300 KB gzipped, the binary under 30 MB (`make bench`).
8. **UI strings** go in `web/src/lib/i18n/en.json` and `ar.json`, not in components. The UI must work right to left.
9. **Tests** come with the change. A new parser or validator of outside input gets a fuzz target.

## Commits and pull requests
- One change per commit, with a [Conventional Commit](https://www.conventionalcommits.org/) message: `fix(netinfo): count IPv6 listeners once`.
- `make check` passes on every commit. Run `make itest` when you touch the broker, packaging, systemd units or anything a container test covers.
- Keep pull requests small and say what you tested. CI runs `make check`; it needs no secret.
- Do not put anything about your own machine in fixtures, tests or screenshots: no host names, addresses, serial numbers or real container names. Use invented values (`example.test`, `192.0.2.0/24`, `2001:db8::/32`). Screenshots come from `make screenshots`, which uses invented data only.

## Where things are
- `cmd/hearthd`, `cmd/hearth-broker`, `cmd/hearth`: the daemon, the root broker and the CLI.
- `internal/`: one package per subject (`collect`, `services`, `docker`, `stacks`, `automations`, …).
- `web/`: the Svelte UI. `packaging/`: the `.deb`, systemd units and polkit policy.
- `test/itest`, `test/e2e`: the container tests and the browser tests.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) explains how the parts fit.

<!-- Read CONTRIBUTING.md first. A security fix goes through a private advisory, not a pull request. -->

**What this changes and why**

**How it was tested**
- [ ] `make check` passes
- [ ] `make itest` passes (broker, packaging, systemd units or container-tested code)

**Checklist**
- [ ] Nothing in `docs/SECURITY-INVARIANTS.md` is weakened
- [ ] No shell, no `{@html}`, no new root path outside polkit or the broker
- [ ] New UI strings are in `en.json` and `ar.json`
- [ ] A new dependency is justified in the commit body, and `make notices` was run
- [ ] No data from a real machine in fixtures, tests or screenshots

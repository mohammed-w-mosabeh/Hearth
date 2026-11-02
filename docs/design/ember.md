# Ember design spec (v1.4)

Visual and navigation redesign only: no new backend capability, privilege or network access. Tasks are in `docs/ROADMAP.md`, Phase 8; the source of truth is `docs/plan/phases-6-8.md` and, for the Phase 9 and 10 screens, `docs/plan/phases-9-10.md`.

**Status: prototype (task 8.1).** Add `?ui=ember` to the address, before the `#`, to see it. Without it the current UI is unchanged. Task 8.11 removes the switch and the old shell.

## Principles
- Calm first: a machine with nothing wrong should look quiet.
- Amber means live or active, never decoration.
- Status is never colour alone.
- Numbers line up: mono, tabular figures.
- Every list keeps its place: details open beside it, not instead of it.

## Tokens (`web/src/styles/ember.css`)
| Role | Dark (primary) | Light |
|---|---|---|
| Background | `#14120F` | `#F7F3EC` |
| Surface | `#1C1915` | `#FFFDF8` |
| Raised | `#24201B` | `#EFE9DF` |
| Border | `#38322A` | `#DCD3C5` |
| Text | `#EDE6DA` | `#1F1B16` |
| Muted text | `#A39A8C` | `#635A4E` |
| Accent | `#F59E0B` | `#A84F05` |
| Ok | `#6FCF8E` | `#1B7038` |
| Warning | `#F2C14E` | `#8A5A00` |
| Critical | `#F28B82` | `#B3261E` |
| Info | `#8AB4F8` | `#1F5FA8` |

The ember gradient is `#F59E0B` to `#EA580C`. The light accent departs from the plan's amber because `#F59E0B` cannot reach AA contrast on a light background. `web/src/lib/contrast.test.ts` holds every text and status pair to WCAG AA on the real token file, in both themes: 4.5:1 for text, 3:1 for focus rings and chart lines. Settings offers Dark (the default), Light or System, per browser, beside the density toggle.

## Status
`StatusMark.svelte` pairs the colour with the shape and gives screen readers the words. Lists adopt it in task 8.8.

| Shape | Meaning |
|---|---|
| ● | ok |
| ◐ | degraded |
| ▲ | warning |
| ■ | stopped or failed |

## Typography
IBM Plex Sans and IBM Plex Sans Arabic for UI, JetBrains Mono with tabular figures for numbers, units and code. Bundled and subset, loaded by the browser only when drawn; `check-forbidden.sh` holds all of them to 250 KB. Sources are in `web/src/assets/fonts/README.md`.

## Navigation
A rail on the inline-start side, 224 px open and 56 px collapsed, remembered per browser, collapsed automatically below 900 px.

| Group | Items |
|---|---|
| Overview | Home |
| Workloads | Desktop apps, Apps, Docker, Services, Games |
| System | Resources, Disks, Network, Packages, Tools, Space advisor, Hardware, Terminal, Settings |
| Automation | Scheduler, Alerts, Modes |
| Insight & security | Security, Changes, Reports, Journal, Heat and energy, Tokens |

A group folds from its heading, remembered per browser; the group holding the page on screen stays open, so the rail fits a short screen. Every `g` shortcut and the Ctrl+K palette reach all groups. Every existing route keeps its URL. Two groups differ from the plan because those pages exist and the plan had no place for them: Space advisor and Terminal join System, Tokens joins Insight & security. Boot analysis and Connectivity stay inside their current pages. Apps (`#/stacks`) opens the Docker page's Compose tab and Modes (`#/modes`) the Profiles page until Phases 9 and 10 ship.

## Page anatomy
1. **Pulse strip:** a sticky 32 px bar with CPU, RAM, temperature, network and battery, in mono. A click opens Resources. The prototype samples like Home; the 15 s rate and jumping to a metric come in task 8.5.
2. **Page head:** title, one line of context, page actions at the inline end.
3. **Content:** cards and tables on tokens. Home gains the health ring and Needs attention in task 8.6.
4. **Inspector drawer:** on the inline-end side, 420 px, flipping in RTL. Task 8.7.

## Components
- Buttons: one primary per view. Danger buttons are outlined, so they never look like the safe default. A disabled button is visibly dimmed.
- Danger notice: a critical edge, a ▲ and plain words saying what can happen.
- Chips: small state labels. Warning and critical chips carry a shape.
- Tables: row height from the density token, sticky headers in task 8.8.
- Secrets: masked dots, with a Reveal button per value.

## Motion
Transitions of at most 150 ms. One pulse on a live dot per update. Nothing animates under `prefers-reduced-motion`.

## Right to left
Logical CSS only; the rail and drawer flip. Code, logs and charts stay left to right. A value inside an Arabic sentence is isolated so it keeps its own order.

## Fixture screens
`web/design.html` is a development-only entry for the Phase 9 and 10 screens, served by `vite` at `/design.html?screen=<id>`. `vite build` reads `index.html` alone and `check-forbidden.sh` refuses any import of `web/src/design` from production code, so no mock data can ship. Screen ids: `apps-list`, `app-drawer`, `app-page`, `import`, `deploy`, `backup`, `modes`, `mode-editor`, `mode-revert`, `approval`.

## Screenshots
`docs/design/screens/`, from `test/e2e/design/capture.mjs`, with invented data only.
- `<page>.<theme>.<density>.<dir>.jpg`: the current UI and Ember side by side, for Home, Containers, Services, Network and Security.
- `fixture.<screen>.<theme>.<dir>.jpg`: the Phase 9 and 10 screens.

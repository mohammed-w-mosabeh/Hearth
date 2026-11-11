# Ember design spec (v1.4)

Visual and navigation redesign only: no new backend capability, privilege or network access. Tasks are in `docs/ROADMAP.md`, Phase 8; the source of truth is `docs/plan/phases-6-8.md` and, for the Phase 9 and 10 screens, `docs/plan/phases-9-10.md`.

**Status: shipped (task 8.11).** Ember is Hearth's only UI. The `?ui=ember` switch, the old sidebar and the old tokens are gone; old links with `?ui=ember` still open, as the parameter is ignored.

## Principles
- Calm first: a machine with nothing wrong should look quiet.
- Amber means live or active, never decoration.
- Status is never colour alone.
- Numbers line up: mono, tabular figures.
- Every list keeps its place: details open beside it, not instead of it.

## Tokens (`web/src/styles/tokens.css`)
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
| ■ | failed |
| □ | stopped |

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

A group folds from its heading, remembered per browser; the group holding the page on screen stays open, so the rail fits a short screen. Every `g` shortcut and the Ctrl+K palette reach all groups. Every existing route keeps its URL. Two groups differ from the plan because those pages exist and the plan had no place for them: Space advisor and Terminal join System, Tokens joins Insight & security. Boot analysis and Connectivity stay inside their current pages. Apps (`#/stacks`) has its own page since Phase 9. Modes (`#/modes`) is a route of its own since task 10.2 and has replaced the Profiles page since 10.9; `#/profiles` redirects to it. The Modes page follows the fixture screens `modes`, `mode-editor`, `mode-revert` and `approval`: pending approvals on top, a table of modes (session settings, managed apps and others, triggers armed by a checkbox), the editor as a dialog, and the revert view as the confirmation that lists what goes back and what stays because it was changed by hand.

## Page anatomy
1. **Pulse strip:** a sticky 32 px bar with CPU, RAM, temperature, network and battery, in mono. Each value opens its chart on Resources. It reads the sampler's ring every 15 s, and follows a page already at 1 s, without raising the rate itself.
2. **Page head:** title, one line of context, page actions at the inline end.
3. **Content:** cards and tables on tokens. Home opens with the health ring and Needs attention, then Quick views (the card grid).
4. **Inspector drawer:** on the inline-end side, 420 px, flipping in RTL. See below.

## Home health (task 8.6)
The ring is 100 minus the points below, never under 0. Each item takes fixed points and each kind has a cap; nothing else is weighed, and selecting the ring lists the deductions that apply and these rules. The queue holds the same items, the most serious first, eight shown until "Show more". Code: `web/src/lib/health.ts`.

| Kind | Read from | Points each | Cap | Row action |
| --- | --- | --- | --- | --- |
| Failed unit | `GET /services` | 10 | 30 | Restart, with the Services page's preview and confirmation |
| Critical alert firing | `GET /alerts/events` (the newest 500), live on the `alerts` topic | 15 | 30 | Open alerts |
| Warning alert firing | same | 5 | 15 | Open alerts |
| Info alert firing | same | 0 | 0 | Open alerts |
| Security updates waiting | `GET /security` | 15 | 15 | Open updates |
| Listener on every address, no active firewall | `GET /security` | 5 | 15 | Security page |
| Restart needed (reboot or services) | `GET /security` | 5 | 5 | Security page |
| Disk full within 7 days | `GET /forecasts` | 15 | 30 | Space advisor |
| Disk reporting failure (SMART) | `GET /disks/health` | 25 | 50 | Disk health |
| Hardening index lower than the run before | `GET /security/lynis` | 10 | 10 | Security page |
| Unhealthy app: a container unhealthy, restarting or dead, or exited while the app should run (9.12) | `GET /app-health` | 10 | 30 | Apps page |
| App whose last backup failed (9.12) | same | 15 | 30 | Apps page |
| App the owner's last update preview found a new digest for (9.12) | same | 5 | 10 | Apps page |
| Pending mode approval: stack steps an automatic switch left for a click (10.7) | `GET /modes` (`approvals`) | 0 | 0 | Open modes |
| Pending automation approval: steps an automation left for a click (12.9) | `GET /automations/approvals` (`automations`) | 0 | 0 | Open automations |

The update row never asks a registry itself (SECURITY S21a): it repeats what the owner's last update preview of the app found, kept in the `stacks.updates` setting, and lapses once the app's spec or a running image changes. A failed backup's row is keyed by the backup's time, so a snooze covers that failure only. A listener behind an active firewall is info on the Security page and is not counted here. A source that cannot be read deducts nothing and is named as not checked. Everything is read when Home opens and every 5 minutes after; alerts also arrive as they fire. A row snoozes for 24 hours, per viewer, in the `home.snoozed` setting (at most 200 items, each `{"until": {"<item id>": <unix ms>}}`); a snoozed item still counts in the ring. On Ember, Home's restart banner gives way to the restart row.

## Inspector drawer (task 8.7)
Selecting an item in a list opens it in a drawer on the inline-end side instead of leaving the page. The hash carries it, `#/<page>?inspect=<kind>:<id>`, so a drawer can be linked, survives a reload, and Back returns to the previous item. Code: `web/src/components/Inspector.svelte`, `web/src/components/inspect/`, ids in `web/src/lib/inspect.ts`.

| Kind | Opened from | Id | Mini chart | Actions |
| --- | --- | --- | --- | --- |
| Unit | Services | `unit:<system\|user>/<name>` | CPU and memory from `services` | start, stop, restart |
| Container | Docker, Compose | `container:<id>` | CPU and memory from `docker.stats` | start, stop, restart |
| Package | Packages (apt) | `package:<name>[:<arch>]` | none | on the Packages page |
| Port | Network, "What is this?" | `port:<tcp\|udp>/<port>` | none | stop the unit or container, or end the process |
| Process group | Resources, Processes view | `proc:<group id>` | CPU and memory from `procs` | end |
| Alert | Alerts | `alert:<event id>` | none | on the Alerts page |

Each drawer has its facts, the chart where a live series exists (it fills while the drawer stays open, since no per-item history is stored before task 8.8), its actions through the actions registry with the same preview and confirmation as the page, a link to the full page, and the lineage as a chain (port → process → unit → package → container) in which each link opens that item in the drawer. The drawer is a modal `<dialog>`: focus stays inside, Esc closes it, and focus goes back to what opened it. A link that names no item opens and says so. Packages of other managers keep their dialog.

## Lists (task 8.8)
Services, Docker containers and Resources' processes (apps, services and containers grouped) share one pattern:
- **Status column:** the first column holds the row's shape (see Status). Units: ● active, ◐ changing, ■ failed, □ inactive. Containers: ● running, ◐ paused or changing (▲ when unhealthy), ■ failed or exited with an error, □ stopped. Process groups: ● running, ◐ paused.
- **CPU, 10 min:** a 64×16 SVG line per row, under a faint frame of the full 10 minutes, from 0 to the row's own peak. Plain SVG, not uPlot, so a list of 900 units stays cheap. It fills while the list follows its topic (`services`, `docker.stats`, `procs`) and lasts for the session; containers start with the 5 minutes hearthd already keeps. No other per-item history is stored. Gaps over a minute, when the list was not open, break the line. Time runs left to right in both directions.
- **Sticky header:** the table scrolls in its own box (at most 72% of the window's height), and its header stays at the top.
- **Keyboard:** one row is in the Tab order. ↑ ↓ Home End move between rows, Enter opens the row in the drawer, and clicking or tabbing into a row makes it the tab stop.

## Components
- Buttons: one primary per view. Danger buttons are outlined, so they never look like the safe default. A disabled button is visibly dimmed.
- Danger notice: a critical edge, a ▲ and plain words saying what can happen.
- Chips: small state labels. Warning and critical chips carry a shape.
- Tables: row height from the density token, sticky headers in task 8.8.
- Secrets: masked dots, with a Reveal button per value.

## Motion
Transitions of at most 150 ms, all through `--motion`. The live dot at the foot of the rail pulses once for each WebSocket update: it is recreated per update and scales down from 1.75× with transform and opacity only, so it stays on the compositor. Nothing loops. Under `prefers-reduced-motion`, `--motion` is 0 and a global rule turns every animation and transition off. `src/lib/motion.test.ts` checks every stylesheet and component for both.

## States
- Loading: `Skeleton`, a muted line saying what is being read over two or three still bars in the rough shape of the content, with `role="status"` and `aria-busy`. Nothing shimmers.
- Empty: `EmptyState`. A whole page uses it centred. A list with nothing in it yet uses it inline, with an icon, a dashed edge, why it is empty and how to fill it, plus an action where one exists (New job).
- Error: what failed, then what to do next. `failure()` in `src/lib/errors.ts` takes the server's message and adds a next step by HTTP status: 400, 413 and 422 check the input; 403 group membership; 404 reload; 409 wait and reload; 429 wait; 502 to 504 the service Hearth relies on; anything else hearthd's log. When no answer came at all, it says to check that Hearth is running. `src/lib/errors.test.ts` refuses a bare server message or a load error rendered without its next step.

## Accessibility
- axe-core, with no serious or critical violation, on every page, dark and light, left to right and right to left. `test/e2e/tests/a11y.spec.ts` runs it in `make itest` on the real hearthd, with the hostile strings seeded. `test/e2e/design/a11y.mjs` runs it on fixtures, with the drawer, the palette, the shortcuts list and a row menu open, and on every Phase 9 and 10 screen. axe is injected through the DevTools protocol, so the CSP stays as it is.
- Focus: a 2 px ring in `--focus` on `:focus-visible`, held to 3:1 on every background in both themes. The palette's search field shows it as an underline. Nothing removes a ring without a replacement.
- Dialogs are native modal `<dialog>`s, so focus stays inside and Esc closes. Focus goes back where it was, and from a row menu that is the row's ⋯ button, because a closing popover hands focus back. The drawer's keys are tested in `xss.spec.ts`.
- Terminal tabs follow the WAI-ARIA tabs pattern. Only the current tab is in the Tab order, the arrow keys move (flipped right to left), and Home, End and Delete work. The × beside a tab is for the mouse and sits outside the tab list's tree.
- The keyboard reaches every region that scrolls: a `<pre>` takes focus itself, and a table box is reached through its rows.
- Fills that carry text use a matching `-contrast` token: `--accent-contrast` on the accent, `--crit-contrast` on critical (error toasts, the confirm button of a destructive action).

## Right to left
Logical CSS only; the rail and drawer flip. Code, logs and charts stay left to right. A value inside an Arabic sentence is isolated so it keeps its own order.

## Fixture screens
`web/design.html` is a development-only entry for the Phase 9 and 10 screens, served by `vite` at `/design.html?screen=<id>`. `vite build` reads `index.html` alone and `check-forbidden.sh` refuses any import of `web/src/design` from production code, so no mock data can ship. Screen ids: `apps-list`, `app-drawer`, `app-page`, `import`, `deploy`, `backup`, `modes`, `mode-editor`, `mode-revert`, `approval`.

## Screenshots
`docs/design/screens/`, from `test/e2e/design/capture.mjs`, with invented data only.
- `<page>.<theme>.<density>.<dir>.jpg`: the old UI (from 9c3b245, the last commit with it) and Ember side by side, for Home, Containers, Services, Network and Security. `capture.mjs` takes the old side from a second dev server (`BEFORE_URL`); without one it shoots Ember alone.
- `fixture.<screen>.<theme>.<dir>.jpg`: the Phase 9 and 10 screens.

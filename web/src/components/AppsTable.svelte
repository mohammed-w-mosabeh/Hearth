<script lang="ts">
  import CapDialog from './CapDialog.svelte';
  import RowSpark from './RowSpark.svelte';
  import StatusMark from './StatusMark.svelte';
  import { groupStatus, rowNav, sparks } from '../lib/rows';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Blocked, type CapField, type Params, type Plan, type Result } from '../lib/actions';
  import { ApiError } from '../lib/api';
  import { bytes, pct, rate } from '../lib/format';
  import { toast } from '../lib/toast.svelte';
  import { menuLeft, type MessageKey, t } from '../lib/i18n';
  import { emberRequested } from '../lib/ember';
  import { inspectHref } from '../lib/router.svelte';
  import { view, type KindFilter, type SortKey } from '../lib/procs';
  import type { GroupKind, ProcSnapshot } from '../lib/types';
  import type { Live } from '../lib/ws';

  // Processes grouped by app, unit or container. Following the "procs"
  // topic is what makes hearthd scan /proc, so it happens only while this
  // table is on screen.
  let { live }: { live: Live } = $props();

  // On Ember a group's name opens the inspector drawer (ROADMAP 8.7).
  const ember = emberRequested(location.search);

  let snap = $state.raw<ProcSnapshot | null>(null);
  // Each group's CPU over the last 10 minutes, kept for the session.
  let spark = $state.raw(sparks.procs.snapshot());
  $effect(() =>
    live.subscribe('procs', (data) => {
      const s = data as ProcSnapshot;
      if (!s || !Array.isArray(s.groups)) return;
      snap = s;
      if (s.partial) return; // the first scan has no CPU yet
      const now = Date.parse(s.t) / 1000 || Date.now() / 1000;
      for (const g of s.groups) sparks.procs.add(g.id, now, g.cpu);
      sparks.procs.prune(now);
      spark = sparks.procs.snapshot();
    }),
  );

  let search = $state('');
  let kind = $state<KindFilter>('all');
  let sortKey = $state<SortKey>('cpu');
  let desc = $state(true);
  let open = $state<Record<string, boolean>>({});

  const rows = $derived(snap ? view(snap.groups, kind, search, sortKey, desc) : []);

  function sortBy(k: SortKey): void {
    if (sortKey === k) desc = !desc;
    else {
      sortKey = k;
      desc = k !== 'name';
    }
  }

  const columns: { key: SortKey; label: MessageKey; num: boolean }[] = [
    { key: 'name', label: 'apps.col.name', num: false },
    { key: 'cpu', label: 'apps.col.cpu', num: true },
    { key: 'mem', label: 'apps.col.mem', num: true },
    { key: 'read', label: 'apps.col.read', num: true },
    { key: 'write', label: 'apps.col.write', num: true },
    { key: 'count', label: 'apps.col.count', num: true },
  ];
  const filters: { id: KindFilter; label: MessageKey }[] = [
    { id: 'all', label: 'apps.filter.all' },
    { id: 'apps', label: 'apps.filter.apps' },
    { id: 'services', label: 'apps.filter.services' },
    { id: 'containers', label: 'apps.filter.containers' },
    { id: 'other', label: 'apps.filter.other' },
  ];
  const kindLabels: Record<GroupKind, MessageKey> = {
    app: 'apps.kind.app',
    service: 'apps.kind.service',
    'user-service': 'apps.kind.userService',
    container: 'apps.kind.container',
    session: 'apps.kind.session',
    kernel: 'apps.kind.kernel',
  };
  // Actions (ROADMAP 1.9). One shared menu (a popover="auto" panel: top
  // layer, light dismiss, Esc) and one confirmation dialog for all rows.
  type Destructive = 'terminate' | 'kill';
  type Subject = { target: string; name: string; group: boolean; stopped: boolean; unit?: string | undefined };

  let menu: HTMLDivElement;
  let subject = $state<Subject | null>(null);
  let pending = $state<{ action: Destructive; subject: Subject; plan: Plan } | null>(null);

  const verbs: Record<string, MessageKey> = {
    terminate: 'action.terminate',
    kill: 'action.kill',
    freeze: 'action.freeze',
    resume: 'action.resume',
    renice: 'action.renice',
    ionice: 'action.ionice',
    cap: 'action.cap',
  };

  function openMenu(e: MouseEvent, s: Subject): void {
    subject = s;
    const btn = e.currentTarget as HTMLElement;
    menu.showPopover();
    // Anchor positioning is not available in the target browsers yet:
    // place the panel from the button's box, flipping at the edges.
    const r = btn.getBoundingClientRect();
    const w = menu.offsetWidth;
    const h = menu.offsetHeight;
    const top = r.bottom + 4 + h > innerHeight ? Math.max(4, r.top - 4 - h) : r.bottom + 4;
    const left = menuLeft(r, w);
    menu.style.top = `${top}px`;
    menu.style.left = `${left}px`;
  }

  // Caps (ROADMAP 5.2) act on the user's own units, until they end.
  let capOf = $state<Subject | null>(null);
  let capError = $state('');
  const capFields: CapField[] = [
    { key: 'MemoryMax', label: 'cap.memory' },
    { key: 'CPUQuota', label: 'cap.cpu' },
  ];
  const capUnit = (g: { kind: string; unit?: string }) =>
    (g.kind === 'app' || g.kind === 'user-service') && g.unit ? g.unit : undefined;

  async function saveCap(params: Params): Promise<void> {
    const s = capOf;
    if (!s) return;
    try {
      const res = await run('unit', 'cap', `user/${s.unit}`, params);
      capOf = null;
      toast(describe('cap', s.name, res), { tone: res.done === 0 ? 'error' : 'info' });
    } catch (err) {
      capError = errorText(err);
    }
  }

  function errorText(err: unknown): string {
    return err instanceof ApiError ? err.message : t('action.failed');
  }

  // describe turns a result into one line: what was done, what was not.
  function describe(action: string, name: string, res: Result): string {
    const parts = [t('result.done', { verb: t(verbs[action] ?? 'action.failed'), name, n: res.done })];
    const skipped: Blocked[] = [...res.blocked, ...res.failed];
    if (skipped.length > 0) {
      const first = skipped[0] as Blocked;
      parts.push(t('result.skipped', { n: skipped.length, reason: t(reasonKey(first.reason), { user: first.detail ?? '', detail: first.detail ?? '' }) }));
    }
    return parts.join(' ');
  }

  async function act(action: string, s: Subject, params: Record<string, string> = {}): Promise<void> {
    menu.hidePopover();
    try {
      const res = await run('proc', action, s.target, params);
      const undo = action === 'freeze' && res.done > 0 ? () => void act('resume', s) : undefined;
      toast(describe(action, s.name, res), { tone: res.done === 0 ? 'error' : 'info', ...(undo ? { undo } : {}) });
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function confirmFirst(action: Destructive, s: Subject): Promise<void> {
    menu.hidePopover();
    try {
      const plan = await preview('proc', action, s.target);
      if (plan.items.length === 0) {
        toast(describe(action, s.name, { done: 0, failed: [], blocked: plan.blocked }), { tone: 'error' });
        return;
      }
      pending = { action, subject: s, plan };
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { action, subject: s, plan } = pending;
    try {
      const res = await run('proc', action, s.target, {}, plan.token, typed || undefined);
      toast(describe(action, s.name, res), { tone: res.done === 0 ? 'error' : 'info' });
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      pending = null;
    }
  }

  const cpuText = (v: number) => (snap?.partial ? '–' : pct(v, 1));
  const ioText = (v: number | undefined) => (v === undefined ? '–' : rate(v));
</script>

<div class="bar">
  <input type="search" placeholder={t('apps.search')} aria-label={t('apps.search')} bind:value={search} />
  <div class="segmented" role="radiogroup" aria-label={t('apps.filter')}>
    {#each filters as f (f.id)}
      <button type="button" role="radio" aria-checked={kind === f.id} class:on={kind === f.id} onclick={() => (kind = f.id)}>
        {t(f.label)}
      </button>
    {/each}
  </div>
</div>

{#if !snap}
  <p class="note">{t('apps.loading')}</p>
{:else if rows.length === 0}
  <p class="note">{t('apps.none')}</p>
{:else}
  <div class="scroll table-scroll">
    <table>
      <thead>
        <tr>
          <th class="st"><span class="visually-hidden">{t('rows.status')}</span></th>
          {#each columns as c (c.key)}
            <th class:num={c.num} aria-sort={sortKey === c.key ? (desc ? 'descending' : 'ascending') : 'none'}>
              <button type="button" onclick={() => sortBy(c.key)}>
                {t(c.label)}{#if sortKey === c.key}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}
              </button>
            </th>
          {/each}
          <th><span class="th">{t('rows.spark')}</span></th>
          <th><span class="visually-hidden">{t('apps.col.actions')}</span></th>
        </tr>
      </thead>
      <tbody use:rowNav>
        {#each rows as g (g.id)}
          {@const gs = groupStatus(g)}
          <tr class="group" aria-label={g.name}>
            <td class="st"><StatusMark kind={gs} label={t(gs === 'degraded' ? 'rows.paused' : 'rows.running')} /></td>
            <td>
              <button
                type="button"
                class="expand"
                data-primary={ember ? undefined : true}
                aria-expanded={open[g.id] === true}
                aria-label={t(open[g.id] ? 'apps.collapse' : 'apps.expand', { name: g.name })}
                onclick={() => (open[g.id] = !open[g.id])}>{open[g.id] ? '▾' : '▸'}</button
              >
              {#if ember}
                <a class="name" data-primary title={g.unit ?? g.name} href={inspectHref(`proc:${g.id}`)}>{g.name}</a>
              {:else}
                <span class="name" title={g.unit ?? g.name}>{g.name}</span>
              {/if}
              <span class="tag">{t(kindLabels[g.kind])}</span>
              <span class="user">{g.user}</span>
            </td>
            <td class="num">{cpuText(g.cpu)}</td>
            <td class="num">{bytes(g.mem)}</td>
            <td class="num" title={g.io_partial ? t('apps.ioPartial') : undefined}>{ioText(g.read_bps)}{g.io_partial ? '*' : ''}</td>
            <td class="num" title={g.io_partial ? t('apps.ioPartial') : undefined}>{ioText(g.write_bps)}{g.io_partial ? '*' : ''}</td>
            <td class="num">{g.count}</td>
            <td><RowSpark points={spark[g.id] ?? []} /></td>
            <td class="act">
              <button
                type="button"
                class="more-btn"
                aria-label={t('apps.actionsFor', { name: g.name })}
                onclick={(e) =>
                  openMenu(e, { target: `group:${g.id}`, name: g.name, group: true, unit: capUnit(g), stopped: g.procs.length > 0 && g.procs.every((p) => p.state === 'T') })}
                >⋯</button
              >
            </td>
          </tr>
          {#if open[g.id]}
            {#each g.procs as p (p.pid)}
              <tr class="proc">
                <td></td>
                <td><span class="pid">{p.pid}</span> {p.name} <span class="user">{p.state}</span></td>
                <td class="num">{cpuText(p.cpu)}</td>
                <td class="num" title={t('apps.rss')}>{bytes(p.rss)}</td>
                <td class="num">–</td>
                <td class="num">–</td>
                <td></td>
                <td></td>
                <td class="act">
                  <button
                    type="button"
                    class="more-btn"
                    aria-label={t('apps.actionsFor', { name: `${p.name} (${p.pid})` })}
                    onclick={(e) => openMenu(e, { target: `pid:${p.pid}:${p.start}`, name: `${p.name} (${p.pid})`, group: false, stopped: p.state === 'T' })}
                    >⋯</button
                  >
                </td>
              </tr>
            {/each}
            {#if g.count > g.procs.length}
              <tr class="proc"><td colspan="9" class="more">{t('apps.more', { n: g.count - g.procs.length })}</td></tr>
            {/if}
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
  <p class="foot">{t('apps.footnote')}</p>
{/if}

<!-- Not a role="menu": a group of buttons in a popover (no arrow-key contract). -->
<div bind:this={menu} popover="auto" class="menu" aria-label={subject ? t('apps.actionsFor', { name: subject.name }) : undefined}>
  {#if subject}
    {@const s = subject}
    {#if s.stopped}
      <button type="button" onclick={() => act('resume', s)}>{t('menu.resume')}</button>
    {:else}
      <button type="button" onclick={() => act('freeze', s)}>{t('menu.freeze')}</button>
    {/if}
    <button type="button" onclick={() => confirmFirst('terminate', s)}>{t('menu.terminate')}</button>
    <button type="button" class="danger" onclick={() => confirmFirst('kill', s)}>{t('menu.kill')}</button>
    <hr />
    <span class="label">{t('menu.priority')}</span>
    <button type="button" onclick={() => act('renice', s, { nice: '0' })}>{t('menu.prioNormal')}</button>
    <button type="button" onclick={() => act('renice', s, { nice: '10' })}>{t('menu.prioLow')}</button>
    <button type="button" onclick={() => act('renice', s, { nice: '19' })}>{t('menu.prioLowest')}</button>
    <hr />
    <span class="label">{t('menu.disk')}</span>
    <button type="button" onclick={() => act('ionice', s, { class: 'none' })}>{t('menu.diskNormal')}</button>
    <button type="button" onclick={() => act('ionice', s, { class: 'idle' })}>{t('menu.diskIdle')}</button>
    {#if s.unit}
      <hr />
      <button
        type="button"
        onclick={() => {
          menu.hidePopover();
          capError = '';
          capOf = s;
        }}>{t('menu.cap')}</button
      >
    {/if}
  {/if}
</div>

<CapDialog
  open={capOf !== null}
  title={capOf ? t('cap.title', { name: capOf.name }) : ''}
  intro={t('cap.intro')}
  fields={capFields}
  error={capError}
  onsave={saveCap}
  oncancel={() => (capOf = null)}
/>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t(pending.action === 'kill' ? 'confirm.killTitle' : 'confirm.terminateTitle', { name: pending.subject.name }) : ''}
  verb={pending ? t(pending.action === 'kill' ? 'menu.kill' : 'menu.terminate') : ''}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<style>
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-4);
  }
  input[type='search'] {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    min-width: 220px;
  }
  .segmented {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow: hidden;
  }
  .segmented button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 0;
    border-inline-start: 1px solid var(--border);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .segmented button:first-child {
    border-inline-start: 0;
  }
  .segmented button.on {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .note,
  .foot {
    color: var(--text-muted);
  }
  .foot {
    font-size: 12px;
    margin-top: var(--space-2);
  }
  .scroll {
    overflow-x: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  th,
  td {
    padding: 0 var(--space-3);
    height: var(--row-h);
    text-align: start;
    white-space: nowrap;
    border-bottom: 1px solid var(--border);
  }
  th {
    position: sticky;
    top: 0;
    background: var(--surface);
  }
  th button {
    font: inherit;
    font-weight: 600;
    color: var(--text-muted);
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .num {
    text-align: end;
  }
  tr.proc td {
    background: var(--surface-2);
    color: var(--text-muted);
    height: 30px;
  }
  tr.proc td:first-child {
    padding-inline-start: 40px;
  }
  .expand {
    font: inherit;
    background: none;
    border: 0;
    color: var(--text-muted);
    cursor: pointer;
    width: 20px;
    padding: 0;
  }
  .name {
    display: inline-block;
    max-width: 32ch;
    overflow: hidden;
    text-overflow: ellipsis;
    vertical-align: bottom;
  }
  .tag {
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
  }
  .user,
  .pid {
    color: var(--text-muted);
    font-size: 12px;
    margin-inline-start: var(--space-2);
  }
  .pid {
    margin: 0;
    font-family: var(--font-mono);
  }
  .more {
    font-style: italic;
  }
  .act {
    width: 1%;
    text-align: end;
  }
  .more-btn {
    font: inherit;
    font-size: 16px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 1px solid transparent;
    border-radius: var(--radius-s);
    padding: 2px 8px;
    cursor: pointer;
  }
  .more-btn:hover,
  .more-btn:focus-visible {
    border-color: var(--border);
    color: var(--text);
  }
  .menu {
    position: fixed;
    inset: auto;
    margin: 0;
    min-width: 200px;
    padding: var(--space-1);
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    font-size: 13px;
  }
  .menu:popover-open {
    display: grid;
  }
  .menu button {
    font: inherit;
    text-align: start;
    color: var(--text);
    background: none;
    border: 0;
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .menu button:hover,
  .menu button:focus-visible {
    background: var(--surface-2);
  }
  .menu .danger {
    color: var(--crit);
  }
  .menu hr {
    border: 0;
    border-top: 1px solid var(--border);
    margin: var(--space-1) 0;
    width: 100%;
  }
  .menu .label {
    padding: var(--space-1) var(--space-3) 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
  a.name {
    color: inherit;
    text-decoration: none;
  }
  a.name:hover,
  a.name:focus-visible {
    text-decoration: underline;
  }
  .st {
    width: 1.5em;
    padding-inline-end: 0;
    text-align: center;
  }
</style>

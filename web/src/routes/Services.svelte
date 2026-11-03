<script lang="ts">
  import Skeleton from '../components/Skeleton.svelte';
  import { failure } from '../lib/errors';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import RowSpark from '../components/RowSpark.svelte';
  import StatusMark from '../components/StatusMark.svelte';
  import { rowNav, sparks, unitStatus } from '../lib/rows';
  import JournalDialog from '../components/JournalDialog.svelte';
  import OverrideDialog from '../components/OverrideDialog.svelte';
  import { preview, reasonKey, run, type Params, type Plan, type Result } from '../lib/actions';
    import { bytes, pct } from '../lib/format';
  import { menuLeft, type MessageKey, t } from '../lib/i18n';
  import {
    available,
    failedCount,
    health,
    key,
    view,
    type ScopeFilter,
    type ServicesMsg,
    type SortKey,
    type Stat,
    type StateFilter,
    type TypeFilter,
    type Unit,
    type UnitOp,
  } from '../lib/services';
  import { toast } from '../lib/toast.svelte';
  import { inspectHref, router } from '../lib/router.svelte';
  import { emberRequested } from '../lib/ember';
  import type { Live } from '../lib/ws';

  // systemd units, system and user. Following the "services" topic is what
  // makes hearthd talk to systemd, so it happens only while this page is
  // open.
  let { live }: { live: Live } = $props();

  // On Ember a unit's name opens the inspector drawer (ROADMAP 8.7).
  const ember = emberRequested(location.search);

  let units = $state.raw<Unit[] | null>(null);
  let errors = $state.raw<Partial<Record<string, string>>>({});
  let stats = $state.raw<Record<string, Stat>>({});
  // Each unit's CPU over the last 10 minutes, kept for the session.
  let spark = $state.raw(sparks.units.snapshot());
  let filesKnown = $state(false);

  $effect(() =>
    live.subscribe('services', (data) => {
      const m = data as ServicesMsg;
      if (m?.kind === 'list' && Array.isArray(m.units)) {
        units = m.units;
        errors = m.errors ?? {};
        filesKnown = m.units.some((u) => u.enabled);
      } else if (m?.kind === 'stats' && m.stats) {
        stats = m.stats;
        const now = Date.now() / 1000;
        for (const [k, s] of Object.entries(m.stats)) sparks.units.add(k, now, s.cpu);
        sparks.units.prune(now);
        spark = sparks.units.snapshot();
      }
    }),
  );

  let search = $state('');
  let scope = $state<ScopeFilter>('all');
  let type = $state<TypeFilter>('service');
  let stateF = $state<StateFilter>('all');
  let sortKey = $state<SortKey>('name');
  let desc = $state(false);

  // Opened from the command palette (#/services?q=<unit>): show that unit.
  $effect(() => {
    if (router.q) {
      search = router.q;
      scope = 'all';
      type = 'all';
      stateF = 'all';
    }
  });

  const rows = $derived(units ? view(units, stats, { scope, type, state: stateF, search }, sortKey, desc) : []);
  const failed = $derived(units ? failedCount(units) : 0);

  function sortBy(k: SortKey): void {
    if (sortKey === k) desc = !desc;
    else {
      sortKey = k;
      desc = k === 'mem' || k === 'cpu';
    }
  }

  function showFailed(): void {
    scope = 'all';
    type = 'all';
    stateF = 'failed';
    search = '';
  }

  const columns: { key: SortKey | null; label: MessageKey; num: boolean }[] = [
    { key: 'name', label: 'svc.col.unit', num: false },
    { key: 'state', label: 'svc.col.state', num: false },
    { key: null, label: 'svc.col.enabled', num: false },
    { key: 'mem', label: 'svc.col.mem', num: true },
    { key: 'cpu', label: 'svc.col.cpu', num: true },
  ];
  const scopes: { id: ScopeFilter; label: MessageKey }[] = [
    { id: 'all', label: 'svc.scope.all' },
    { id: 'system', label: 'svc.scope.system' },
    { id: 'user', label: 'svc.scope.user' },
  ];
  const states: { id: StateFilter; label: MessageKey }[] = [
    { id: 'all', label: 'svc.state.all' },
    { id: 'running', label: 'svc.state.running' },
    { id: 'failed', label: 'svc.state.failed' },
    { id: 'inactive', label: 'svc.state.inactive' },
  ];
  const types: { id: TypeFilter; label: MessageKey }[] = [
    { id: 'service', label: 'svc.type.service' },
    { id: 'timer', label: 'svc.type.timer' },
    { id: 'socket', label: 'svc.type.socket' },
    { id: 'mount', label: 'svc.type.mount' },
    { id: 'other', label: 'svc.type.other' },
    { id: 'all', label: 'svc.type.all' },
  ];

  const statOf = (u: Unit) => stats[key(u)];

  // Actions (ROADMAP 1.13). One shared menu, one confirmation dialog for
  // critical units (the server asks for it: the preview comes back
  // destructive with the name to type), and the log viewer.
  let menu: HTMLDivElement;
  let subject = $state<Unit | null>(null);
  let pending = $state<{ op: UnitOp; unit: Unit; plan: Plan; params?: Params } | null>(null);
  let limitsOf = $state<Unit | null>(null);
  let limitsError = $state('');
  let journalOf = $state<Unit | null>(null);
  let busy = $state.raw<ReadonlySet<string>>(new Set());

  const opLabels: Record<UnitOp, MessageKey> = {
    start: 'svc.op.start',
    stop: 'svc.op.stop',
    restart: 'svc.op.restart',
    enable: 'svc.op.enable',
    disable: 'svc.op.disable',
    mask: 'svc.op.mask',
    unmask: 'svc.op.unmask',
    override: 'svc.op.override',
  };
  const doneLabels: Record<UnitOp, MessageKey> = {
    start: 'svc.done.start',
    stop: 'svc.done.stop',
    restart: 'svc.done.restart',
    enable: 'svc.done.enable',
    disable: 'svc.done.disable',
    mask: 'svc.done.mask',
    unmask: 'svc.done.unmask',
    override: 'svc.done.override',
  };
  // Undo for the reversible ones (SPEC: toast with Undo for 5 s).
  const undoOf: Partial<Record<UnitOp, UnitOp>> = { stop: 'start', enable: 'disable', disable: 'enable', mask: 'unmask', unmask: 'mask' };

  function openMenu(e: MouseEvent, u: Unit): void {
    subject = u;
    const btn = e.currentTarget as HTMLElement;
    menu.showPopover();
    const r = btn.getBoundingClientRect();
    const w = menu.offsetWidth;
    const h = menu.offsetHeight;
    const top = r.bottom + 4 + h > innerHeight ? Math.max(4, r.top - 4 - h) : r.bottom + 4;
    const left = menuLeft(r, w);
    menu.style.top = `${top}px`;
    menu.style.left = `${left}px`;
  }

  function errorText(err: unknown): string {
    return failure(err, 'action.failed');
  }

  function setBusy(u: Unit, on: boolean): void {
    const k = key(u);
    busy = on ? new Set([...busy, k]) : new Set([...busy].filter((x) => x !== k));
  }

  function report(op: UnitOp, u: Unit, res: Result): void {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done === 0 && bad) {
      // An unclassified error carries systemd's own message.
      const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
      toast(t('svc.failed', { op: t(opLabels[op]), name: u.name, reason }), { tone: 'error' });
      return;
    }
    const back = undoOf[op];
    toast(t(doneLabels[op], { name: u.name }), back ? { undo: () => void execute(back, u) } : {});
  }

  async function execute(op: UnitOp, u: Unit, token?: string, typed?: string, params: Params = {}): Promise<void> {
    setBusy(u, true);
    if (u.scope === 'system') toast(t('svc.authHint'));
    try {
      report(op, u, await run('unit', op, key(u), params, token, typed));
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      setBusy(u, false);
    }
  }

  async function act(op: UnitOp, u: Unit): Promise<void> {
    menu.hidePopover();
    if (op === 'override') {
      limitsError = '';
      limitsOf = u;
      return;
    }
    if (op === 'start' || op === 'enable') return execute(op, u);
    try {
      const plan = await preview('unit', op, key(u));
      if (plan.items.length === 0) {
        report(op, u, { done: 0, failed: [], blocked: plan.blocked });
        return;
      }
      if (plan.risk === 'destructive') pending = { op, unit: u, plan };
      else await execute(op, u);
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { op, unit: u, plan, params } = pending;
    pending = null;
    await execute(op, u, plan.token, typed, params);
  }

  async function saveLimits(params: Params): Promise<void> {
    const u = limitsOf;
    if (!u) return;
    try {
      const plan = await preview('unit', 'override', key(u), params);
      limitsOf = null;
      if (plan.items.length === 0) report('override', u, { done: 0, failed: [], blocked: plan.blocked });
      else if (plan.risk === 'destructive') pending = { op: 'override', unit: u, plan, params };
      else await execute('override', u, undefined, undefined, params);
    } catch (err) {
      limitsError = errorText(err);
    }
  }

  function logs(u: Unit): void {
    menu.hidePopover();
    journalOf = u;
  }
</script>

<header>
  <h1>{t('svc.title')}</h1>
  <p>{units ? t('svc.count', { n: units.length }) : t('svc.intro')}</p>
</header>

{#if failed > 0}
  <div class="banner" role="status">
    <span>{t('svc.failedBanner', { n: failed })}</span>
    <button type="button" onclick={showFailed}>{t('svc.showFailed')}</button>
  </div>
{/if}
{#each Object.entries(errors) as [s, msg] (s)}
  <p class="note error" role="status">{t('svc.error', { scope: s, msg: msg ?? '' })}</p>
{/each}

<div class="bar">
  <input type="search" placeholder={t('svc.search')} aria-label={t('svc.search')} bind:value={search} />
  <div class="segmented" role="radiogroup" aria-label={t('svc.scope')}>
    {#each scopes as s (s.id)}
      <button type="button" role="radio" aria-checked={scope === s.id} class:on={scope === s.id} onclick={() => (scope = s.id)}>
        {t(s.label)}
      </button>
    {/each}
  </div>
  <div class="segmented" role="radiogroup" aria-label={t('svc.state')}>
    {#each states as s (s.id)}
      <button type="button" role="radio" aria-checked={stateF === s.id} class:on={stateF === s.id} onclick={() => (stateF = s.id)}>
        {t(s.label)}
      </button>
    {/each}
  </div>
  <select aria-label={t('svc.type')} bind:value={type}>
    {#each types as ty (ty.id)}
      <option value={ty.id}>{t(ty.label)}</option>
    {/each}
  </select>
</div>

{#if units === null}
  <Skeleton label={t('svc.loading')} />
{:else if rows.length === 0}
  <p class="note">{t('svc.none')}</p>
{:else}
  <div class="scroll table-scroll">
    <table>
      <thead>
        <tr>
          <th class="st"><span class="visually-hidden">{t('rows.status')}</span></th>
          {#each columns as c (c.label)}
            <th class:num={c.num} aria-sort={c.key && sortKey === c.key ? (desc ? 'descending' : 'ascending') : 'none'}>
              {#if c.key}
                {@const k = c.key}
                <button type="button" onclick={() => sortBy(k)}>
                  {t(c.label)}{#if sortKey === k}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}
                </button>
              {:else}
                <span class="th">{t(c.label)}</span>
              {/if}
            </th>
          {/each}
          <th><span class="th">{t('rows.spark')}</span></th>
          <th><span class="visually-hidden">{t('svc.col.actions')}</span></th>
        </tr>
      </thead>
      <tbody use:rowNav>
        {#each rows as u (key(u))}
          {@const h = health(u)}
          {@const st = statOf(u)}
          <tr aria-label={u.name}>
            <td class="st"><StatusMark kind={unitStatus(u.active)} label={u.active} /></td>
            <td class="unit">
              {#if ember}
                <a class="name" data-primary title={u.name} href={inspectHref(`unit:${u.scope}/${u.name}`)}>{u.name}</a>
              {:else}
                <span class="name" title={u.name}>{u.name}</span>
              {/if}
              {#if u.scope === 'user'}<span class="tag">{t('svc.tag.user')}</span>{/if}
              {#if u.critical}<span class="tag crit" title={t('svc.criticalHint')}>{t('svc.tag.critical')}</span>{/if}
              {#if u.description && u.description !== u.name}<span class="desc" title={u.description}>{u.description}</span>{/if}
            </td>
            <td>
              <span class:failed={h === 'failed'}>{u.active}</span>
              <span class="sub">{u.sub}{u.job ? ` · ${u.job}…` : ''}</span>
              {#if u.load && u.load !== 'loaded'}<span class="sub">({u.load})</span>{/if}
            </td>
            <td class="muted">{u.enabled ?? (filesKnown ? '–' : '…')}</td>
            <td class="num">{st ? bytes(st.mem) : '–'}</td>
            <td class="num">{st ? pct(st.cpu, 1) : '–'}</td>
            <td><RowSpark points={spark[key(u)] ?? []} /></td>
            <td class="act">
              <button
                type="button"
                class="more-btn"
                data-primary={ember ? undefined : true}
                aria-label={t('svc.actionsFor', { name: u.name })}
                aria-busy={busy.has(key(u))}
                onclick={(e) => openMenu(e, u)}>{busy.has(key(u)) ? '…' : '⋯'}</button
              >
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="foot">{t('svc.footnote')}</p>
{/if}

<!-- Not a role="menu": a group of buttons in a popover (no arrow-key contract). -->
<div bind:this={menu} popover="auto" class="menu" aria-label={subject ? t('svc.actionsFor', { name: subject.name }) : undefined}>
  {#if subject}
    {@const u = subject}
    {#each available(u) as op (op)}
      <button type="button" class:danger={u.critical && op !== 'start' && op !== 'enable' && op !== 'unmask'} onclick={() => act(op, u)}>
        {t(opLabels[op])}
      </button>
    {/each}
    {#if available(u).length > 0}<hr />{/if}
    <button type="button" onclick={() => logs(u)}>{t('svc.op.logs')}</button>
  {/if}
</div>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('svc.confirmTitle', { op: t(opLabels[pending.op]), name: pending.unit.name }) : ''}
  verb={pending ? t(opLabels[pending.op]) : ''}
  warning={t('svc.criticalWarning')}
  body={t('svc.confirmBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<JournalDialog unit={journalOf} onclose={() => (journalOf = null)} />

<OverrideDialog unit={limitsOf} error={limitsError} onsave={saveLimits} oncancel={() => (limitsOf = null)} />

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .banner {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    margin-bottom: var(--space-3);
    border: 1px solid var(--crit);
    border-radius: var(--radius-s);
    color: var(--crit);
    font-size: 13px;
  }
  .banner button {
    font: inherit;
    color: var(--crit);
    background: none;
    border: 1px solid var(--crit);
    border-radius: var(--radius-s);
    padding: 0 var(--space-3);
    cursor: pointer;
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-4);
  }
  input[type='search'],
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
  }
  input[type='search'] {
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
  .foot,
  .muted {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
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
  th button,
  .th {
    font: inherit;
    font-weight: 600;
    color: var(--text-muted);
    background: none;
    border: 0;
    padding: 0;
  }
  th button {
    cursor: pointer;
  }
  .num {
    text-align: end;
  }
  .unit {
    max-width: 0;
    width: 50%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .name {
    font-weight: 500;
  }
  .desc,
  .sub {
    color: var(--text-muted);
    font-size: 12px;
    margin-inline-start: var(--space-2);
  }
  .tag {
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
  }
  .tag.crit {
    color: var(--warn);
    border-color: var(--warn);
  }
  .failed {
    color: var(--crit);
    font-weight: 500;
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
  }
  .menu {
    position: fixed;
    inset: auto;
    margin: 0;
    min-width: 180px;
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

<script lang="ts">
  import CapDialog from '../components/CapDialog.svelte';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import ContainerDetail from '../components/ContainerDetail.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import ComposeProjects from '../components/ComposeProjects.svelte';
  import DockerObjects from '../components/DockerObjects.svelte';
  import DockerCleanup from '../components/DockerCleanup.svelte';
  import ImagesTable from '../components/ImagesTable.svelte';
  import { preview, reasonKey, run, type CapField, type Params, type Plan, type Result } from '../lib/actions';
  import { api, ApiError } from '../lib/api';
  import {
    available,
    imageName,
    latest,
    mergeStats,
    portText,
    portURL,
    ports,
    problemCount,
    tone,
    view,
    type Container,
    type ContainerOp,
    type DockerMsg,
    type Point,
    type Snapshot,
    type SortKey,
    type StateFilter,
    type StatsMsg,
  } from '../lib/docker';
  import { bytes, pct } from '../lib/format';
  import { menuLeft, type MessageKey, t } from '../lib/i18n';
  import { router } from '../lib/router.svelte';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Containers (ROADMAP 2.4). Following "docker" is what makes hearthd ask
  // hearth-broker for Docker events, and "docker.stats" for usage, so both
  // happen only while this page is open.
  let { live }: { live: Live } = $props();

  let snap = $state.raw<Snapshot | null>(null);
  let history = $state.raw<Record<string, Point[]>>({});

  $effect(() =>
    live.subscribe('docker', (data) => {
      const m = data as DockerMsg;
      if (m?.kind === 'snapshot' && Array.isArray(m.containers)) snap = m;
    }),
  );
  $effect(() =>
    live.subscribe('docker.stats', (data) => {
      const m = data as StatsMsg;
      if (m?.kind === 'history' || m?.kind === 'sample') history = mergeStats(history, m);
    }),
  );

  async function retry(): Promise<void> {
    try {
      snap = await api<Snapshot>('/docker');
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    }
  }

  type Section = 'containers' | 'compose' | 'images' | 'networks' | 'volumes' | 'cleanup';
  let section = $state<Section>('containers');
  const sections: { id: Section; label: MessageKey }[] = [
    { id: 'containers', label: 'docker.section.containers' },
    { id: 'compose', label: 'docker.section.compose' },
    { id: 'images', label: 'docker.section.images' },
    { id: 'networks', label: 'docker.section.networks' },
    { id: 'volumes', label: 'docker.section.volumes' },
    { id: 'cleanup', label: 'docker.section.cleanup' },
  ];

  function sectionCount(id: Section, s: Snapshot): number {
    if (id === 'images') return s.images.length;
    if (id === 'compose') return s.projects.length;
    if (id === 'networks') return s.networks.length;
    if (id === 'volumes') return s.volumes.length;
    return containers.length;
  }

  let search = $state('');
  let stateF = $state<StateFilter>('all');
  let sortKey = $state<SortKey>('name');
  let desc = $state(false);

  // Opened from the command palette (#/docker?q=<name>).
  $effect(() => {
    if (router.q) {
      search = router.q;
      stateF = 'all';
    }
  });

  const stats = $derived(latest(history));
  const containers = $derived(snap?.state === 'ok' ? snap.containers : []);
  const rows = $derived(view(containers, stats, stateF, search, sortKey, desc));
  const problems = $derived(problemCount(containers));

  function sortBy(k: SortKey): void {
    if (sortKey === k) desc = !desc;
    else {
      sortKey = k;
      desc = k === 'mem' || k === 'cpu' || k === 'created';
    }
  }

  const columnsDef: { key: SortKey | null; label: MessageKey; num: boolean }[] = [
    { key: 'name', label: 'docker.col.name', num: false },
    { key: null, label: 'docker.col.image', num: false },
    { key: 'state', label: 'docker.col.state', num: false },
    { key: null, label: 'docker.col.ports', num: false },
    { key: 'cpu', label: 'docker.col.cpu', num: true },
    { key: 'mem', label: 'docker.col.mem', num: true },
  ];
  const filters: { id: StateFilter; label: MessageKey }[] = [
    { id: 'all', label: 'docker.filter.all' },
    { id: 'running', label: 'docker.filter.running' },
    { id: 'stopped', label: 'docker.filter.stopped' },
    { id: 'problem', label: 'docker.filter.problem' },
  ];

  // Actions: one shared menu; remove is previewed and confirmed (the name
  // is typed when the container is running).
  let menu: HTMLDivElement;
  let subject = $state<Container | null>(null);
  let pending = $state<{ c: Container; plan: Plan } | null>(null);
  let detailId = $state<string | null>(null);
  let busy = $state.raw<ReadonlySet<string>>(new Set());

  const detail = $derived(detailId ? (containers.find((c) => c.id === detailId) ?? null) : null);

  const opLabels: Record<ContainerOp, MessageKey> = {
    start: 'docker.op.start',
    stop: 'docker.op.stop',
    restart: 'docker.op.restart',
    pause: 'docker.op.pause',
    unpause: 'docker.op.unpause',
    remove: 'docker.op.remove',
    limit: 'docker.op.limit',
  };
  const doneLabels: Record<ContainerOp, MessageKey> = {
    start: 'docker.done.start',
    stop: 'docker.done.stop',
    restart: 'docker.done.restart',
    pause: 'docker.done.pause',
    unpause: 'docker.done.unpause',
    remove: 'docker.done.remove',
    limit: 'docker.done.limit',
  };
  const undoOf: Partial<Record<ContainerOp, ContainerOp>> = { stop: 'start', pause: 'unpause' };

  function openMenu(e: MouseEvent, c: Container): void {
    subject = c;
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
    return err instanceof ApiError ? err.message : t('action.failed');
  }

  function setBusy(c: Container, on: boolean): void {
    busy = on ? new Set([...busy, c.id]) : new Set([...busy].filter((x) => x !== c.id));
  }

  function reasonText(reason: string, detailText?: string): string {
    if (reason === 'not-found') return t('docker.reason.notFound');
    if (reason === 'error' && detailText) return detailText;
    return t(reasonKey(reason), { detail: detailText ?? '', user: '' });
  }

  function report(op: ContainerOp, c: Container, res: Result): void {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done === 0 && bad) {
      toast(t('docker.failed', { op: t(opLabels[op]), name: c.name, reason: reasonText(bad.reason, bad.detail) }), { tone: 'error' });
      return;
    }
    if (op === 'remove' && detailId === c.id) detailId = null;
    const back = undoOf[op];
    toast(t(doneLabels[op], { name: c.name }), back ? { undo: () => void execute(back, c) } : {});
  }

  async function execute(op: ContainerOp, c: Container, token?: string, typed?: string): Promise<void> {
    setBusy(c, true);
    toast(t('docker.authHint'));
    try {
      report(op, c, await run('container', op, c.id, {}, token, typed));
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      setBusy(c, false);
    }
  }

  async function act(op: ContainerOp, c: Container): Promise<void> {
    menu.hidePopover();
    if (op !== 'remove') return execute(op, c);
    try {
      const plan = await preview('container', op, c.id);
      if (plan.items.length === 0) {
        report(op, c, { done: 0, failed: [], blocked: plan.blocked });
        return;
      }
      pending = { c, plan };
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { c, plan } = pending;
    pending = null;
    await execute('remove', c, plan.token, typed);
  }

  let limitsOf = $state<Container | null>(null);
  let limitsError = $state('');
  const limitFields: CapField[] = [
    { key: 'memory', label: 'docker.limit.memory' },
    { key: 'cpus', label: 'docker.limit.cpus' },
    { key: 'restart', label: 'docker.limit.restart', options: ['no', 'always', 'unless-stopped', 'on-failure'], none: 'docker.limit.keep' },
  ];

  async function saveLimits(params: Params): Promise<void> {
    const c = limitsOf;
    if (!c) return;
    try {
      const res = await run('container', 'limit', c.id, params);
      limitsOf = null;
      report('limit', c, res);
    } catch (err) {
      limitsError = errorText(err);
    }
  }

  function openDetail(c: Container): void {
    menu.hidePopover();
    detailId = c.id;
  }

  function showProblems(): void {
    stateF = 'problem';
    search = '';
  }

  const healthKey: Record<string, MessageKey> = {
    healthy: 'docker.health.healthy',
    unhealthy: 'docker.health.unhealthy',
    starting: 'docker.health.starting',
  };
</script>

{#if snap && snap.state !== 'ok'}
  {@const s = snap.state}
  <EmptyState
    title={t(`docker.state.${s}.title` as MessageKey)}
    body={s === 'error' ? (snap.error ?? '') : t(`docker.state.${s}.body` as MessageKey)}
  >
    <button type="button" class="plain" onclick={retry}>{t('docker.retry')}</button>
  </EmptyState>
{:else}
  <header>
    <h1>{t('docker.title')}</h1>
    <p>{snap ? t('docker.version', { version: snap.version ?? '?', n: containers.length }) : t('docker.intro')}</p>
  </header>

  <div class="sections" role="tablist" aria-label={t('docker.sections')}>
    {#each sections as sec (sec.id)}
      <button type="button" role="tab" aria-selected={section === sec.id} class:on={section === sec.id} onclick={() => (section = sec.id)}>
        {t(sec.label)}{#if snap && sec.id !== 'cleanup'}<span class="count">{sectionCount(sec.id, snap)}</span>{/if}
      </button>
    {/each}
  </div>

  {#if section === 'cleanup'}
    <DockerCleanup />
  {:else if section === 'images' || section === 'compose' || section === 'networks' || section === 'volumes'}
    {#if snap === null}
      <p class="note">{t('docker.loading')}</p>
    {:else if section === 'images'}
      <ImagesTable images={snap.images} {containers} />
    {:else if section === 'networks'}
      <DockerObjects kind="network" networks={snap.networks} volumes={snap.volumes} {containers} />
    {:else if section === 'volumes'}
      <DockerObjects kind="volume" networks={snap.networks} volumes={snap.volumes} {containers} />
    {:else}
      <ComposeProjects projects={snap.projects} {containers} onopen={(c) => (detailId = c.id)} />
    {/if}
  {:else}
  {#if problems > 0}
    <div class="banner" role="status">
      <span>{t('docker.problemBanner', { n: problems })}</span>
      <button type="button" onclick={showProblems}>{t('docker.showProblems')}</button>
    </div>
  {/if}

  <div class="bar">
    <input type="search" placeholder={t('docker.search')} aria-label={t('docker.search')} bind:value={search} />
    <div class="segmented" role="radiogroup" aria-label={t('docker.filter')}>
      {#each filters as f (f.id)}
        <button type="button" role="radio" aria-checked={stateF === f.id} class:on={stateF === f.id} onclick={() => (stateF = f.id)}>
          {t(f.label)}
        </button>
      {/each}
    </div>
  </div>

  {#if snap === null}
    <p class="note">{t('docker.loading')}</p>
  {:else if containers.length === 0}
    <p class="note">{t('docker.none')}</p>
  {:else if rows.length === 0}
    <p class="note">{t('docker.noMatch')}</p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr>
            {#each columnsDef as col (col.label)}
              <th class:num={col.num} aria-sort={col.key && sortKey === col.key ? (desc ? 'descending' : 'ascending') : 'none'}>
                {#if col.key}
                  {@const k = col.key}
                  <button type="button" onclick={() => sortBy(k)}>
                    {t(col.label)}{#if sortKey === k}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}
                  </button>
                {:else}
                  <span class="th">{t(col.label)}</span>
                {/if}
              </th>
            {/each}
            <th><span class="visually-hidden">{t('docker.col.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as c (c.id)}
            {@const tn = tone(c)}
            {@const st = stats[c.id]}
            {@const up = c.state === 'running' || c.state === 'paused'}
            <tr>
              <td class="name-cell">
                <button type="button" class="link" onclick={() => openDetail(c)}>{c.name}</button>
                {#if c.project}<span class="tag" title={t('docker.tag.project', { project: c.project })}>{c.project}</span>{/if}
              </td>
              <td class="muted image" title={c.image}>{imageName(c)}</td>
              <td>
                <span class="dot {tn}" aria-hidden="true"></span>
                <span class:failed={tn === 'failed'}>{c.status}</span>
                {#if c.health && !c.status.includes(c.health)}<span class="sub">{t(healthKey[c.health] ?? 'docker.health.starting')}</span>{/if}
              </td>
              <td><span class="ports">
                {#each ports(c).slice(0, 3) as p (portText(p))}
                  {@const url = portURL(p)}
                  {#if url}
                    <a href={url} target="_blank" rel="noopener noreferrer" title={t('docker.openPort', { port: portText(p) })}>{portText(p)}</a>
                  {:else}
                    <span class="muted">{portText(p)}</span>
                  {/if}
                {/each}
                {#if ports(c).length > 3}<span class="muted">+{ports(c).length - 3}</span>{/if}
              </span></td>
              <td class="num">{up && st ? pct(st.cpu, 1) : '–'}</td>
              <td class="num">{up && st ? bytes(st.mem) : '–'}</td>
              <td class="act">
                <button
                  type="button"
                  class="more-btn"
                  aria-label={t('docker.actionsFor', { name: c.name })}
                  aria-busy={busy.has(c.id)}
                  onclick={(e) => openMenu(e, c)}>{busy.has(c.id) ? '…' : '⋯'}</button
                >
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="foot">{t('docker.footnote')}</p>
  {/if}
  {/if}
{/if}

<!-- Not a role="menu": a group of buttons in a popover (no arrow-key contract). -->
<div bind:this={menu} popover="auto" class="menu" aria-label={subject ? t('docker.actionsFor', { name: subject.name }) : undefined}>
  {#if subject}
    {@const c = subject}
    <button type="button" onclick={() => openDetail(c)}>{t('docker.op.details')}</button>
    {#if available(c).length > 0}<hr />{/if}
    {#each available(c) as op (op)}
      <button type="button" class:danger={op === 'remove'} onclick={() => act(op, c)}>{t(opLabels[op])}</button>
    {/each}
    {#if c.state !== 'removing'}
      <button
        type="button"
        onclick={() => {
          menu.hidePopover();
          limitsError = '';
          limitsOf = c;
        }}>{t('docker.op.limit')}</button
      >
    {/if}
  {/if}
</div>

<CapDialog
  open={limitsOf !== null}
  title={limitsOf ? t('docker.limit.title', { name: limitsOf.name }) : ''}
  intro={t('docker.limit.intro')}
  fields={limitFields}
  error={limitsError}
  onsave={saveLimits}
  oncancel={() => (limitsOf = null)}
/>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('docker.confirmTitle', { name: pending.c.name }) : ''}
  verb={t('docker.op.remove')}
  {...pending?.plan.confirm ? { warning: t('docker.confirmRunning') } : {}}
  body={t('docker.confirmBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<ContainerDetail container={detail} history={detail ? (history[detail.id] ?? []) : []} {live} onclose={() => (detailId = null)} />

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
  .sections {
    display: flex;
    gap: var(--space-1);
    border-bottom: 1px solid var(--border);
    margin-bottom: var(--space-4);
  }
  .sections button {
    font: inherit;
    font-size: 14px;
    color: var(--text-muted);
    background: none;
    border: 0;
    border-bottom: 2px solid transparent;
    padding: var(--space-2) var(--space-3);
    cursor: pointer;
  }
  .sections button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .count {
    font-size: 12px;
    color: var(--text-muted);
    margin-inline-start: var(--space-2);
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
  .plain {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .note,
  .foot,
  .muted {
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
  .name-cell {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .link {
    font: inherit;
    font-weight: 500;
    color: var(--text);
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .link:hover,
  .link:focus-visible {
    text-decoration: underline;
  }
  .image {
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ports {
    display: inline-flex;
    gap: var(--space-2);
    align-items: center;
    font-size: 12px;
  }
  .ports a {
    color: var(--accent);
  }
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
  .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    margin-inline-end: var(--space-2);
    background: var(--border);
  }
  .dot.running {
    background: var(--ok);
  }
  .dot.failed {
    background: var(--crit);
  }
  .dot.changing,
  .dot.paused {
    background: var(--warn);
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
</style>

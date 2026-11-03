<script lang="ts">
  import Skeleton from '../components/Skeleton.svelte';
  import { failure } from '../lib/errors';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import PackageDetail from '../components/PackageDetail.svelte';
  import { preview, run, type Plan, type Result } from '../lib/actions';
  import { api } from '../lib/api';
  import { bytes } from '../lib/format';
  import { ltr, menuLeft, type MessageKey, t } from '../lib/i18n';
  import {
    counts,
    debRows,
    extraManagers,
    extraRows,
    pkgReasonKey,
    removeKind,
    updateCounts,
    view,
    type DebPackage,
    type ExtraList,
    type Manager,
    type ManagerFilter,
    type Row,
    type ShowFilter,
    type SortKey,
    type Update,
  } from '../lib/packages';
  import { openInspect, router } from '../lib/router.svelte';
  import { emberRequested } from '../lib/ember';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Every package manager in one list (SPEC 4). dpkg's list is fetched
  // again when the "pkgs" topic says its database changed; the other
  // managers and the updates when the page opens and after an action.
  let { live }: { live: Live } = $props();

  // On Ember an apt package opens in the inspector drawer (ROADMAP 8.7);
  // the other managers keep their dialog.
  const ember = emberRequested(location.search);

  function openDetail(r: Row): void {
    if (ember && r.manager === 'apt') openInspect(`package:${r.key.slice('apt/'.length)}`);
    else detailOf = r;
  }

  let deb = $state.raw<DebPackage[] | null>(null);
  let debError = $state('');
  let updates = $state.raw<Update[]>([]);
  let updatesError = $state('');
  let updatesLoading = $state(true);
  let extras = $state.raw<Partial<Record<Manager, ExtraList>>>({});

  const msg = failure;

  async function loadDeb(): Promise<void> {
    try {
      deb = (await api<{ packages: DebPackage[] }>('/pkgs')).packages;
      debError = '';
    } catch (err) {
      debError = msg(err, 'pkg.error');
    }
  }

  async function loadUpdates(): Promise<void> {
    updatesLoading = true;
    try {
      updates = (await api<{ updates: Update[] }>('/pkgs/updates')).updates;
      updatesError = '';
    } catch (err) {
      updatesError = msg(err, 'pkg.updates.error');
    } finally {
      updatesLoading = false;
    }
  }

  async function loadExtra(m: Exclude<Manager, 'apt'>): Promise<void> {
    try {
      const l = await api<ExtraList>(`/pkgs/${m}`);
      extras = { ...extras, [m]: l };
    } catch {
      // A manager that cannot be listed is simply not shown.
    }
  }

  $effect(() => {
    void loadDeb();
    void loadUpdates();
    for (const m of extraManagers) void loadExtra(m);
  });

  $effect(() =>
    live.subscribe('pkgs', (data) => {
      if ((data as { changed?: boolean })?.changed) {
        void loadDeb();
        void loadUpdates();
      }
    }),
  );

  const rows = $derived.by(() => {
    const out = deb ? debRows(deb, updates) : [];
    for (const m of extraManagers) {
      const l = extras[m];
      if (l) out.push(...extraRows(m, l));
    }
    return out;
  });
  const perManager = $derived(counts(rows));
  const ups = $derived(updateCounts(updates));
  // Managers shown as tabs: apt, then any other that is installed or has packages.
  const tabs = $derived<Manager[]>(['apt', ...extraManagers.filter((m) => extras[m]?.available || (perManager[m] ?? 0) > 0)]);

  let search = $state('');
  let manager = $state<ManagerFilter>('all');
  let show = $state<ShowFilter>('all');
  let sortKey = $state<SortKey>('name');
  let desc = $state(false);
  let limit = $state(300);

  // Opened from the command palette (#/packages?q=<name>).
  $effect(() => {
    if (router.q) setSearch(router.q);
  });

  function setSearch(q: string): void {
    search = q;
    manager = 'all';
    show = 'all';
  }

  const shown = $derived(view(rows, { manager, show, search }, sortKey, desc));
  $effect(() => {
    void [manager, show, search];
    limit = 300;
  });

  function sortBy(k: SortKey): void {
    if (sortKey === k) desc = !desc;
    else {
      sortKey = k;
      desc = k === 'size';
    }
  }

  const shows: { id: ShowFilter; label: MessageKey }[] = [
    { id: 'all', label: 'pkg.show.all' },
    { id: 'updates', label: 'pkg.show.updates' },
    { id: 'manual', label: 'pkg.show.manual' },
    { id: 'held', label: 'pkg.show.held' },
  ];
  const columns: { key: SortKey | null; label: MessageKey; num: boolean }[] = [
    { key: 'name', label: 'pkg.col.name', num: false },
    { key: null, label: 'pkg.col.version', num: false },
    { key: 'manager', label: 'pkg.col.manager', num: false },
    { key: 'size', label: 'pkg.col.size', num: true },
  ];

  // Actions. Removing and updating are previewed and confirmed; hold and
  // unhold run at once with Undo.
  type Op = 'remove' | 'update' | 'update-all' | 'hold' | 'unhold';
  const opLabels: Record<Op, MessageKey> = {
    remove: 'pkg.op.remove',
    update: 'pkg.op.update',
    'update-all': 'pkg.op.updateAll',
    hold: 'pkg.op.hold',
    unhold: 'pkg.op.unhold',
  };
  const doneLabels: Record<Op, MessageKey> = {
    remove: 'pkg.done.remove',
    update: 'pkg.done.update',
    'update-all': 'pkg.done.updateAll',
    hold: 'pkg.done.hold',
    unhold: 'pkg.done.unhold',
  };

  let menu: HTMLDivElement;
  let subject = $state<Row | null>(null);
  let detailOf = $state<Row | null>(null);
  let pending = $state<{ op: Op; row: Row | null; kind: string; plan: Plan } | null>(null);
  let busy = $state.raw<ReadonlySet<string>>(new Set());
  const ALL = '*';

  function openMenu(e: MouseEvent, r: Row): void {
    subject = r;
    const btn = e.currentTarget as HTMLElement;
    menu.showPopover();
    const b = btn.getBoundingClientRect();
    const w = menu.offsetWidth;
    const h = menu.offsetHeight;
    menu.style.top = `${b.bottom + 4 + h > innerHeight ? Math.max(4, b.top - 4 - h) : b.bottom + 4}px`;
    menu.style.left = `${menuLeft(b, w)}px`;
  }

  function setBusy(k: string, on: boolean): void {
    busy = on ? new Set([...busy, k]) : new Set([...busy].filter((x) => x !== k));
  }

  function reasonText(code: string, detail?: string): string {
    if (code === 'error' && detail) return detail;
    return t(pkgReasonKey(code), { detail: detail ?? '', user: '' });
  }

  function report(op: Op, name: string, res: Result, row: Row | null): void {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done === 0 && bad) {
      toast(t('pkg.failed', { op: t(opLabels[op]), name, reason: reasonText(bad.reason, bad.detail) }), { tone: 'error' });
      return;
    }
    const back = op === 'hold' ? 'unhold' : op === 'unhold' ? 'hold' : null;
    toast(t(doneLabels[op], { name }), back && row ? { undo: () => void execute(back, row, 'package') } : {});
  }

  function afterChange(r: Row | null): void {
    if (r && r.manager !== 'apt') void loadExtra(r.manager);
    else {
      void loadDeb();
      void loadUpdates();
    }
  }

  async function execute(op: Op, r: Row | null, kind: string, token?: string, typed?: string): Promise<void> {
    const k = r?.key ?? ALL;
    setBusy(k, true);
    toast(t(r?.manager === 'snap' || kind === 'package' ? 'pkg.authHint' : 'pkg.userHint'));
    try {
      const res = await run(kind, op, r ? r.id : 'all', {}, token, typed);
      report(op, r?.name ?? t('pkg.allName'), res, r);
      afterChange(r);
    } catch (err) {
      toast(msg(err, 'action.failed'), { tone: 'error' });
    } finally {
      setBusy(k, false);
    }
  }

  async function act(op: Op, r: Row | null): Promise<void> {
    menu.hidePopover();
    const kind = op === 'remove' && r ? removeKind(r.manager) : 'package';
    if (!kind) return;
    if (op === 'hold' || op === 'unhold') return execute(op, r, kind);
    try {
      const plan = await preview(kind, op, r ? r.id : 'all');
      if (plan.items.length === 0) {
        report(op, r?.name ?? t('pkg.allName'), { done: 0, failed: [], blocked: plan.blocked }, r);
        return;
      }
      pending = { op, row: r, kind, plan };
    } catch (err) {
      toast(msg(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { op, row, kind, plan } = pending;
    pending = null;
    await execute(op, row, kind, plan.token, typed);
  }

  function details(r: Row): void {
    menu.hidePopover();
    openDetail(r);
  }

</script>

<header>
  <h1>{t('pkg.title')}</h1>
  <p>{deb ? t('pkg.count', { n: rows.length }) : t('pkg.intro')}</p>
</header>

{#if updatesError}
  <p class="note" role="status">{t('pkg.updates.unavailable', { msg: updatesError })}</p>
{:else if !updatesLoading && ups.all > 0}
  <div class="banner" class:sec={ups.security > 0} role="status">
    <span>
      {t('pkg.updates.banner', { n: ups.all })}{#if ups.security > 0}
        · <strong>{t('pkg.updates.security', { n: ups.security })}</strong>{/if}
    </span>
    <button type="button" onclick={() => (show = 'updates')}>{t('pkg.updates.show')}</button>
    <button type="button" aria-busy={busy.has(ALL)} disabled={busy.has(ALL)} onclick={() => act('update-all', null)}>
      {busy.has(ALL) ? '…' : t('pkg.op.updateAll')}
    </button>
  </div>
{:else if !updatesLoading}
  <p class="note">{t('pkg.updates.none')}</p>
{/if}
{#if debError}<p class="note error" role="status">{debError}</p>{/if}

<div class="bar">
  <input type="search" placeholder={t('pkg.search')} aria-label={t('pkg.search')} bind:value={search} />
  <div class="segmented" role="radiogroup" aria-label={t('pkg.show')}>
    {#each shows as s (s.id)}
      <button type="button" role="radio" aria-checked={show === s.id} class:on={show === s.id} onclick={() => (show = s.id)}>
        {t(s.label)}
      </button>
    {/each}
  </div>
</div>
<div class="tabs" role="tablist" aria-label={t('pkg.managers')}>
  <button type="button" role="tab" aria-selected={manager === 'all'} class:on={manager === 'all'} onclick={() => (manager = 'all')}>
    {t('pkg.mgr.all')} <span class="n">{rows.length}</span>
  </button>
  {#each tabs as m (m)}
    <button type="button" role="tab" aria-selected={manager === m} class:on={manager === m} onclick={() => (manager = m)}>
      {t(`pkg.mgr.${m}`)} <span class="n">{perManager[m] ?? 0}</span>
    </button>
  {/each}
</div>

{#if deb === null && !debError}
  <Skeleton label={t('pkg.loading')} />
{:else if shown.length === 0}
  <p class="note">{t('pkg.none')}</p>
{:else}
  <div class="scroll">
    <table>
      <thead>
        <tr>
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
          <th><span class="visually-hidden">{t('pkg.col.actions')}</span></th>
        </tr>
      </thead>
      <tbody>
        {#each shown.slice(0, limit) as r (r.key)}
          <tr>
            <td class="pname">
              <button type="button" class="link" onclick={() => openDetail(r)}>{r.name}</button>
              {#if r.update?.security}<span class="tag sec">{t('pkg.tag.security')}</span>{/if}
              {#if r.held}<span class="tag">{t('pkg.tag.held')}</span>{/if}
              {#if r.auto}<span class="tag" title={t('pkg.tag.autoHint')}>{t('pkg.tag.auto')}</span>{/if}
              {#if r.summary}<span class="desc" title={r.summary}>{r.summary}</span>{/if}
            </td>
            <td class="ver">
              <span title={r.version}>{r.version || '–'}</span>
              {#if r.update}<span class="new" title={r.update.version}>{ltr(`→ ${r.update.version}`)}</span>{/if}
            </td>
            <td class="muted">{t(`pkg.mgr.${r.manager}`)}</td>
            <td class="num">{r.size >= 0 ? bytes(r.size) : '–'}</td>
            <td class="act">
              <button
                type="button"
                class="more-btn"
                aria-label={t('pkg.actionsFor', { name: r.name })}
                aria-busy={busy.has(r.key)}
                onclick={(e) => openMenu(e, r)}>{busy.has(r.key) ? '…' : '⋯'}</button
              >
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if shown.length > limit}
    <button type="button" class="showmore" onclick={() => (limit += 300)}>{t('pkg.showMore', { n: shown.length - limit })}</button>
  {/if}
  <p class="foot">{t('pkg.footnote')}</p>
{/if}

<!-- Not a role="menu": a group of buttons in a popover (no arrow-key contract). -->
<div bind:this={menu} popover="auto" class="menu" aria-label={subject ? t('pkg.actionsFor', { name: subject.name }) : undefined}>
  {#if subject}
    {@const r = subject}
    <button type="button" onclick={() => details(r)}>{t('pkg.op.details')}</button>
    {#if r.manager === 'apt'}
      {#if r.update && r.update.severity !== 'blocked'}
        <button type="button" onclick={() => act('update', r)}>{t('pkg.op.update')}</button>
      {/if}
      <button type="button" onclick={() => act(r.held ? 'unhold' : 'hold', r)}>{t(r.held ? 'pkg.op.unhold' : 'pkg.op.hold')}</button>
    {/if}
    {#if removeKind(r.manager)}
      <hr />
      {#if r.removable}
        <button type="button" class="danger" onclick={() => act('remove', r)}>{t('pkg.op.remove')}</button>
      {:else}
        <p class="why">{t('pkg.cannotRemove', { reason: reasonText(r.why ?? 'error', r.usedBy?.join(', ')) })}</p>
      {/if}
    {:else if r.why}
      <hr />
      <p class="why">{reasonText(r.why)}</p>
    {/if}
  {/if}
</div>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('pkg.confirmTitle', { op: t(opLabels[pending.op]), name: pending.row?.name ?? t('pkg.allName') }) : ''}
  verb={pending ? t(opLabels[pending.op]) : ''}
  body={pending ? t(pending.op === 'remove' ? 'pkg.confirm.remove' : 'pkg.confirm.update', { n: pending.plan.items.length }) : ''}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<PackageDetail row={detailOf} onclose={() => (detailOf = null)} onsearch={setSearch} />

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
    border: 1px solid var(--accent);
    border-radius: var(--radius-s);
    font-size: 13px;
  }
  .banner.sec {
    border-color: var(--crit);
  }
  .banner.sec strong {
    color: var(--crit);
  }
  .banner span {
    flex: 1;
  }
  .banner button {
    font: inherit;
    color: var(--text);
    background: none;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 0 var(--space-3);
    cursor: pointer;
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-3);
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
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    border-bottom: 1px solid var(--border);
    margin-bottom: var(--space-3);
  }
  .tabs button {
    font: inherit;
    font-size: 13px;
    color: var(--text-muted);
    background: none;
    border: 0;
    border-bottom: 2px solid transparent;
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .tabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .n {
    font-size: 11px;
    color: var(--text-muted);
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
  .pname {
    max-width: 0;
    width: 55%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ver {
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .new {
    color: var(--accent);
    margin-inline-start: var(--space-1);
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
  .desc {
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
  .tag.sec {
    color: var(--crit);
    border-color: var(--crit);
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
  .showmore {
    font: inherit;
    font-size: 13px;
    color: var(--accent);
    background: none;
    border: 0;
    padding: var(--space-2) 0;
    cursor: pointer;
  }
  .menu {
    position: fixed;
    inset: auto;
    margin: 0;
    min-width: 180px;
    max-width: 320px;
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
  .why {
    color: var(--text-muted);
    font-size: 12px;
    margin: 0;
    padding: var(--space-1) var(--space-3);
    white-space: normal;
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

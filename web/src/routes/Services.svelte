<script lang="ts">
  import { bytes, pct } from '../lib/format';
  import { t, type MessageKey } from '../lib/i18n';
  import {
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
  } from '../lib/services';
  import type { Live } from '../lib/ws';

  // systemd units, system and user. Following the "services" topic is what
  // makes hearthd talk to systemd, so it happens only while this page is
  // open.
  let { live }: { live: Live } = $props();

  let units = $state.raw<Unit[] | null>(null);
  let errors = $state.raw<Partial<Record<string, string>>>({});
  let stats = $state.raw<Record<string, Stat>>({});
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
      }
    }),
  );

  let search = $state('');
  let scope = $state<ScopeFilter>('all');
  let type = $state<TypeFilter>('service');
  let stateF = $state<StateFilter>('all');
  let sortKey = $state<SortKey>('name');
  let desc = $state(false);

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
  <p class="note">{t('svc.loading')}</p>
{:else if rows.length === 0}
  <p class="note">{t('svc.none')}</p>
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
        </tr>
      </thead>
      <tbody>
        {#each rows as u (key(u))}
          {@const h = health(u)}
          {@const st = statOf(u)}
          <tr>
            <td class="unit">
              <span class="name" title={u.name}>{u.name}</span>
              {#if u.scope === 'user'}<span class="tag">{t('svc.tag.user')}</span>{/if}
              {#if u.critical}<span class="tag crit" title={t('svc.criticalHint')}>{t('svc.tag.critical')}</span>{/if}
              {#if u.description && u.description !== u.name}<span class="desc" title={u.description}>{u.description}</span>{/if}
            </td>
            <td>
              <span class="dot {h}" aria-hidden="true"></span>
              <span class:failed={h === 'failed'}>{u.active}</span>
              <span class="sub">{u.sub}{u.job ? ` · ${u.job}…` : ''}</span>
              {#if u.load && u.load !== 'loaded'}<span class="sub">({u.load})</span>{/if}
            </td>
            <td class="muted">{u.enabled ?? (filesKnown ? '–' : '…')}</td>
            <td class="num">{st ? bytes(st.mem) : '–'}</td>
            <td class="num">{st ? pct(st.cpu, 1) : '–'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="foot">{t('svc.footnote')}</p>
{/if}

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
  .dot.changing {
    background: var(--warn);
  }
  .failed {
    color: var(--crit);
    font-weight: 500;
  }
</style>

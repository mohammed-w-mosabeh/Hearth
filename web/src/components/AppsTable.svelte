<script lang="ts">
  import { bytes, pct, rate } from '../lib/format';
  import { t, type MessageKey } from '../lib/i18n';
  import { view, type KindFilter, type SortKey } from '../lib/procs';
  import type { GroupKind, ProcSnapshot } from '../lib/types';
  import type { Live } from '../lib/ws';

  // Processes grouped by app, unit or container. Following the "procs"
  // topic is what makes hearthd scan /proc, so it happens only while this
  // table is on screen.
  let { live }: { live: Live } = $props();

  let snap = $state.raw<ProcSnapshot | null>(null);
  $effect(() =>
    live.subscribe('procs', (data) => {
      const s = data as ProcSnapshot;
      if (s && Array.isArray(s.groups)) snap = s;
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
  <div class="scroll">
    <table>
      <thead>
        <tr>
          {#each columns as c (c.key)}
            <th class:num={c.num} aria-sort={sortKey === c.key ? (desc ? 'descending' : 'ascending') : 'none'}>
              <button type="button" onclick={() => sortBy(c.key)}>
                {t(c.label)}{#if sortKey === c.key}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}
              </button>
            </th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#each rows as g (g.id)}
          <tr class="group">
            <td>
              <button
                type="button"
                class="expand"
                aria-expanded={open[g.id] === true}
                aria-label={t(open[g.id] ? 'apps.collapse' : 'apps.expand', { name: g.name })}
                onclick={() => (open[g.id] = !open[g.id])}>{open[g.id] ? '▾' : '▸'}</button
              >
              <span class="name" title={g.unit ?? g.name}>{g.name}</span>
              <span class="tag">{t(kindLabels[g.kind])}</span>
              <span class="user">{g.user}</span>
            </td>
            <td class="num">{cpuText(g.cpu)}</td>
            <td class="num">{bytes(g.mem)}</td>
            <td class="num" title={g.io_partial ? t('apps.ioPartial') : undefined}>{ioText(g.read_bps)}{g.io_partial ? '*' : ''}</td>
            <td class="num" title={g.io_partial ? t('apps.ioPartial') : undefined}>{ioText(g.write_bps)}{g.io_partial ? '*' : ''}</td>
            <td class="num">{g.count}</td>
          </tr>
          {#if open[g.id]}
            {#each g.procs as p (p.pid)}
              <tr class="proc">
                <td><span class="pid">{p.pid}</span> {p.name} <span class="user">{p.state}</span></td>
                <td class="num">{cpuText(p.cpu)}</td>
                <td class="num" title={t('apps.rss')}>{bytes(p.rss)}</td>
                <td class="num">–</td>
                <td class="num">–</td>
                <td></td>
              </tr>
            {/each}
            {#if g.count > g.procs.length}
              <tr class="proc"><td colspan="6" class="more">{t('apps.more', { n: g.count - g.procs.length })}</td></tr>
            {/if}
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
  <p class="foot">{t('apps.footnote')}</p>
{/if}

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
</style>

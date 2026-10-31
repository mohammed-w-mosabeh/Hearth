<script lang="ts">
  import Chart from '../components/Chart.svelte';
  import { api, apiBlob, ApiError } from '../lib/api';
  import { eventDetail, eventKey } from '../lib/changes';
  import { format } from '../lib/charts';
  import { bytes, duration, pct } from '../lib/format';
  import { t } from '../lib/i18n';
  import {
    PERIODS,
    attachmentName,
    chartUnit,
    drawable,
    cpuSeconds,
    filterItems,
    kindLabel,
    reportQuery,
    trendColumns,
    xRange,
    type AlertEvent,
    type Change,
    type Format,
    type ItemReport,
    type ItemTotal,
    type MachineReport,
    type Period,
    type TimelineEvent,
    type WeeklyState,
  } from '../lib/reports';
  import { toast } from '../lib/toast.svelte';

  type View = 'machine' | 'item';

  let view = $state<View>('machine');
  let period = $state<Period>('week');
  let machine = $state.raw<MachineReport | null>(null);
  let item = $state.raw<ItemReport | null>(null);
  let items = $state.raw<ItemTotal[] | null>(null);
  let itemId = $state('');
  let itemSearch = $state('');
  let failed = $state(false);
  let loading = $state(false);
  let weekly = $state.raw<WeeklyState | null>(null);
  let savingWeekly = $state(false);

  // Only the latest request's answer is shown when the period or item
  // changes again before the previous report arrives.
  let seq = 0;
  async function fetchReport(path: string, done: (r: unknown) => void): Promise<void> {
    const mine = ++seq;
    loading = true;
    try {
      const r = await api<unknown>(path);
      if (mine !== seq) return;
      done(r);
      failed = false;
    } catch {
      if (mine === seq) failed = true;
    } finally {
      if (mine === seq) loading = false;
    }
  }

  function loadMachine(p: Period): Promise<void> {
    return fetchReport(`/reports/machine?${reportQuery(p)}`, (r) => (machine = r as MachineReport));
  }

  function loadItem(id: string, p: Period): Promise<void> {
    if (id === '') {
      item = null;
      return Promise.resolve();
    }
    return fetchReport(`/reports/item?${reportQuery(p, id)}`, (r) => (item = r as ItemReport));
  }

  async function loadItems(): Promise<void> {
    try {
      items = await api<ItemTotal[]>('/reports/items');
      if (itemId === '' && items[0]) itemId = items[0].item;
    } catch {
      items = [];
    }
  }

  async function loadWeekly(): Promise<void> {
    try {
      weekly = await api<WeeklyState>('/reports/weekly');
    } catch {
      weekly = null;
    }
  }

  $effect(() => {
    void loadWeekly();
  });
  $effect(() => {
    if (view === 'machine') void loadMachine(period);
  });
  $effect(() => {
    if (view === 'item' && items === null) void loadItems();
  });
  $effect(() => {
    if (view === 'item') void loadItem(itemId, period);
  });

  function openItem(id: string): void {
    itemId = id;
    view = 'item';
  }

  function save(blob: Blob, name: string): void {
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  async function exportAs(fmt: Format): Promise<void> {
    const path =
      view === 'machine' ? `/reports/machine?${reportQuery(period, undefined, fmt)}` : `/reports/item?${reportQuery(period, itemId, fmt)}`;
    try {
      const { blob, disposition } = await apiBlob(path);
      save(blob, attachmentName(disposition, `hearth-report.${fmt}`));
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('reports.exportFailed'), { tone: 'error' });
    }
  }

  async function downloadSaved(name: string): Promise<void> {
    try {
      const { blob, disposition } = await apiBlob(`/reports/saved/${encodeURIComponent(name)}`);
      save(blob, attachmentName(disposition, 'hearth-week.html'));
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('reports.exportFailed'), { tone: 'error' });
    }
  }

  async function setWeekly(enabled: boolean): Promise<void> {
    savingWeekly = true;
    try {
      await api('/reports/weekly', { method: 'POST', body: JSON.stringify({ enabled }) });
      toast(t(enabled ? 'reports.weekly.on' : 'reports.weekly.off'));
      // The first report is written right after it is turned on.
      setTimeout(() => void loadWeekly(), enabled ? 1500 : 0);
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    } finally {
      savingWeekly = false;
    }
  }

  const when = (iso: string | number) => new Date(iso).toLocaleString();
  const shownItems = $derived(filterItems(items ?? [], itemSearch));
  const current = $derived(view === 'machine' ? machine : item);
</script>

{#snippet events(list: AlertEvent[])}
  <div class="table">
    <table>
      <thead>
        <tr>
          <th>{t('reports.col.started')}</th><th>{t('reports.col.rule')}</th><th>{t('reports.col.on')}</th>
          <th>{t('reports.col.severity')}</th><th>{t('reports.col.ended')}</th>
        </tr>
      </thead>
      <tbody>
        {#each list as e (e.id)}
          <tr>
            <td class="t">{when(e.started_at)}</td><td>{e.rule_name}</td><td class="wrap">{e.instance}</td>
            <td class={e.severity}>{e.severity}</td>
            <td class="t">{e.ended_at ? when(e.ended_at) : t('reports.ongoing')}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

{#snippet timeline(list: TimelineEvent[])}
  <div class="table">
    <table>
      <thead>
        <tr>
          <th>{t('reports.col.time')}</th><th>{t('reports.col.what')}</th><th>{t('reports.col.target')}</th>
          <th>{t('reports.col.detail')}</th>
        </tr>
      </thead>
      <tbody>
        {#each list as e, i (i)}
          {@const key = eventKey(e)}
          <tr><td class="t">{when(e.t)}</td><td>{key ? t(key) : (e.action ?? e.kind)}</td><td class="wrap">{e.subject}</td><td class="wrap">{eventDetail(e)}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

{#snippet changes(list: Change[])}
  <div class="table">
    <table>
      <thead>
        <tr>
          <th>{t('reports.col.time')}</th><th>{t('reports.col.action')}</th><th>{t('reports.col.target')}</th>
          <th>{t('reports.col.result')}</th>
        </tr>
      </thead>
      <tbody>
        {#each list as c, i (i)}
          <tr><td class="t">{when(c.t)}</td><td>{c.action}</td><td class="wrap">{c.target}</td><td class="wrap">{c.result}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

<header>
  <h1>{t('reports.title')}</h1>
  <p>{t('reports.intro')}</p>
</header>

<div class="bar no-print">
  <div class="segmented" role="radiogroup" aria-label={t('reports.kind')}>
    <button type="button" role="radio" aria-checked={view === 'machine'} class:on={view === 'machine'} onclick={() => (view = 'machine')}>
      {t('reports.machine')}
    </button>
    <button type="button" role="radio" aria-checked={view === 'item'} class:on={view === 'item'} onclick={() => (view = 'item')}>
      {t('reports.item')}
    </button>
  </div>
  <div class="segmented" role="radiogroup" aria-label={t('reports.period')}>
    {#each PERIODS as p (p.id)}
      <button type="button" role="radio" aria-checked={period === p.id} class:on={period === p.id} onclick={() => (period = p.id)}>
        {t(p.label)}
      </button>
    {/each}
  </div>
  <span class="spacer"></span>
  <button type="button" disabled={current === null} onclick={() => exportAs('html')}>{t('reports.export.html')}</button>
  <button type="button" disabled={current === null} onclick={() => exportAs('md')}>{t('reports.export.md')}</button>
  <button type="button" disabled={current === null} onclick={() => window.print()}>{t('reports.print')}</button>
</div>

{#if view === 'item'}
  <div class="bar no-print">
    <input type="search" placeholder={t('reports.item.search')} aria-label={t('reports.item.search')} bind:value={itemSearch} />
    <select aria-label={t('reports.item.pick')} bind:value={itemId} disabled={!items?.length}>
      {#each shownItems as i (i.item)}
        <option value={i.item}>{i.name} · {t(kindLabel(i.kind))}</option>
      {/each}
    </select>
  </div>
{/if}

{#if failed}
  <p class="note error" role="status">{t('reports.error')}</p>
{:else if view === 'machine'}
  {#if machine === null}
    <p class="note">{t('reports.loading')}</p>
  {:else}
    {@const m = machine}
    <section aria-busy={loading}>
      <p class="sub">
        {m.host} · {t(PERIODS.find((p) => p.id === m.period)?.label ?? 'reports.period.week')}: {when(m.from)} – {when(m.to)}
      </p>
      {#if m.covered < 0.9}
        <p class="note">{t('reports.covered', { pct: pct(m.covered * 100) })}</p>
      {/if}

      <h2>{t('reports.resources')}</h2>
      {#if m.stats.length === 0}
        <p class="note">{t('reports.noData')}</p>
      {:else}
        <div class="table">
          <table>
            <thead>
              <tr><th>{t('reports.col.metric')}</th><th class="n">{t('reports.col.avg')}</th><th class="n">{t('reports.col.peak')}</th><th class="n">{t('reports.col.total')}</th></tr>
            </thead>
            <tbody>
              {#each m.stats as s (s.metric)}
                <tr>
                  <td>{s.label}</td>
                  <td class="n">{format(chartUnit(s.unit), s.avg)}</td>
                  <td class="n">{format(chartUnit(s.unit), s.max)}</td>
                  <td class="n">{s.total !== undefined ? bytes(s.total) : ''}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <div class="charts">
          {#each m.trends.filter(drawable) as tr (tr.metric)}
            <Chart title={tr.label} unit={chartUnit(tr.unit)} keys={[tr.metric]} labels={[tr.label]} cols={trendColumns(tr)}
              xRange={xRange(m.from, m.to)} min={0} max={tr.unit === '%' ? 100 : undefined} height={140} />
          {/each}
        </div>
      {/if}

      <h2>{t('reports.top')}</h2>
      {#if m.top.length === 0}
        <p class="note">{t('reports.top.none')}</p>
      {:else}
        <div class="table">
          <table>
            <thead>
              <tr>
                <th>{t('reports.col.name')}</th><th>{t('reports.col.kind')}</th><th class="n">{t('reports.col.cpuAvg')}</th>
                <th class="n">{t('reports.col.cpuPeak')}</th><th class="n">{t('reports.col.cpuTime')}</th>
                <th class="n">{t('reports.col.memAvg')}</th><th class="n">{t('reports.col.memPeak')}</th>
              </tr>
            </thead>
            <tbody>
              {#each m.top as i (i.item)}
                <tr>
                  <td><button type="button" class="link" onclick={() => openItem(i.item)}>{i.name}</button></td>
                  <td>{t(kindLabel(i.kind))}</td>
                  <td class="n">{pct(i.cpu_avg, 1)}</td><td class="n">{pct(i.cpu_max, 1)}</td>
                  <td class="n">{duration(cpuSeconds(i.busy_hours))}</td>
                  <td class="n">{bytes(i.mem_avg)}</td><td class="n">{bytes(i.mem_max)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      {#if m.disks.length > 0}
        <h2>{t('reports.disks')}</h2>
        <div class="table">
          <table>
            <thead>
              <tr><th>{t('reports.col.mount')}</th><th class="n">{t('reports.col.used')}</th><th class="n">{t('reports.col.size')}</th><th class="n">%</th><th class="n">{t('reports.col.change')}</th></tr>
            </thead>
            <tbody>
              {#each m.disks as d (d.mount)}
                <tr>
                  <td class="wrap">{d.mount}</td><td class="n">{bytes(d.used)}</td><td class="n">{bytes(d.total)}</td><td class="n">{pct(d.pct)}</td>
                  <td class="n">{d.growth === undefined ? '' : `${d.growth < 0 ? '−' : '+'}${bytes(Math.abs(d.growth))}`}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      {#if m.batteries.length > 0}
        <h2>{t('reports.batteries')}</h2>
        <div class="table">
          <table>
            <thead>
              <tr><th>{t('reports.col.battery')}</th><th class="n">{t('reports.col.health')}</th><th class="n">{t('reports.col.cycles')}</th><th class="n">{t('reports.col.charge')}</th></tr>
            </thead>
            <tbody>
              {#each m.batteries as b (b.id)}
                <tr>
                  <td>{b.id}{b.model ? ` · ${b.model}` : ''}</td><td class="n">{pct(b.health_pct)}</td>
                  <td class="n">{b.cycles ?? ''}</td><td class="n">{pct(b.pct)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      <h2>{t('reports.alerts')}</h2>
      {#if m.alerts.length === 0}
        <p class="note">{t('reports.alerts.none')}</p>
      {:else}
        {@render events(m.alerts)}
      {/if}

      <h2>{t('reports.changes')}</h2>
      {#if m.changes.length === 0}
        <p class="note">{t('reports.timeline.none')}</p>
      {:else}
        {@render timeline(m.changes)}
      {/if}
    </section>
  {/if}
{:else if items !== null && items.length === 0}
  <p class="note">{t('reports.items.none')}</p>
{:else if item === null}
  <p class="note">{t('reports.loading')}</p>
{:else}
  {@const it = item}
  <section aria-busy={loading}>
    <h2 class="first">{it.name}</h2>
    <p class="sub">{t(kindLabel(it.kind))} · {it.id} · {when(it.from)} – {when(it.to)}</p>
    {#if it.total === null}
      <p class="note">{t('reports.item.none')}</p>
    {:else}
      <dl class="summary">
        <div><dt>{t('reports.col.cpuAvg')}</dt><dd>{pct(it.total.cpu_avg, 1)}</dd></div>
        <div><dt>{t('reports.col.cpuTime')}</dt><dd>{duration(cpuSeconds(it.total.busy_hours))}</dd></div>
        <div><dt>{t('reports.col.memAvg')}</dt><dd>{bytes(it.total.mem_avg)}</dd></div>
        <div><dt>{t('reports.col.hours')}</dt><dd>{it.total.hours}</dd></div>
        {#if it.peak_cpu}
          <div><dt>{t('reports.peak.cpu')}</dt><dd>{pct(it.peak_cpu.cpu_max, 1)} · {when(it.peak_cpu.t)}</dd></div>
        {/if}
        {#if it.peak_mem}
          <div><dt>{t('reports.peak.mem')}</dt><dd>{bytes(it.peak_mem.mem_max)} · {when(it.peak_mem.t)}</dd></div>
        {/if}
      </dl>
      <div class="charts">
        {#each it.trends.filter(drawable) as tr (tr.metric)}
          <Chart title={tr.label} unit={chartUnit(tr.unit)} keys={[tr.metric]} labels={[tr.label]} cols={trendColumns(tr)}
            xRange={xRange(it.from, it.to)} min={0} height={140} />
        {/each}
      </div>
    {/if}

    <h2>{t('reports.events')}</h2>
    {#if it.events.length === 0}
      <p class="note">{t('reports.events.none')}</p>
    {:else}
      {@render events(it.events)}
    {/if}

    <h2>{t('reports.item.changes')}</h2>
    {#if it.changes.length === 0}
      <p class="note">{t('reports.changes.none')}</p>
    {:else}
      {@render changes(it.changes)}
    {/if}
  </section>
{/if}

<section class="weekly no-print">
  <h2>{t('reports.weekly.title')}</h2>
  <label>
    <input type="checkbox" checked={weekly?.enabled ?? false} disabled={weekly === null || savingWeekly}
      onchange={(e) => setWeekly(e.currentTarget.checked)} />
    {t('reports.weekly.toggle')}
  </label>
  <p class="note">{t('reports.weekly.hint')}</p>
  {#if weekly?.saved?.length}
    <ul class="saved">
      {#each weekly.saved as s (s.name)}
        <li>
          <span>{when(s.time)}</span>
          <span class="muted">{bytes(s.size)}</span>
          <button type="button" class="link" onclick={() => downloadSaved(s.name)}>{t('reports.weekly.download')}</button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p,
  .sub,
  .note,
  .muted {
    color: var(--text-muted);
  }
  header p {
    margin: var(--space-1) 0 0;
  }
  h2 {
    font-size: 15px;
    margin: var(--space-5) 0 var(--space-2);
  }
  h2.first {
    margin-top: 0;
  }
  .error {
    color: var(--crit);
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-4);
  }
  .spacer {
    flex: 1;
  }
  .bar > button,
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
  .bar > button {
    cursor: pointer;
  }
  .bar > button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  select {
    min-width: 280px;
    max-width: 100%;
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
  .table {
    overflow-x: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th,
  td {
    text-align: start;
    padding: var(--space-1) var(--space-3);
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  tr:last-child td {
    border-bottom: 0;
  }
  th {
    color: var(--text-muted);
    font-weight: 600;
  }
  .n {
    text-align: end;
    white-space: nowrap;
  }
  .wrap {
    overflow-wrap: anywhere;
  }
  .t {
    white-space: nowrap;
  }
  td.critical {
    color: var(--crit);
  }
  td.warning {
    color: var(--warn);
  }
  .link {
    font: inherit;
    color: var(--accent);
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
    text-align: start;
  }
  .link:hover {
    text-decoration: underline;
  }
  .charts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: var(--space-4);
    margin-top: var(--space-4);
  }
  .summary {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: var(--space-3);
    margin: 0;
  }
  .summary div {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-2) var(--space-3);
  }
  dt {
    color: var(--text-muted);
    font-size: 12px;
  }
  dd {
    margin: 0;
    font-size: 15px;
  }
  .weekly {
    margin-top: var(--space-6);
    border-top: 1px solid var(--border);
    padding-top: var(--space-2);
  }
  .weekly label {
    display: inline-flex;
    gap: var(--space-2);
    align-items: center;
  }
  .saved {
    list-style: none;
    padding: 0;
    margin: var(--space-2) 0 0;
  }
  .saved li {
    display: flex;
    gap: var(--space-3);
    padding: var(--space-1) 0;
  }
  section[aria-busy='true'] {
    opacity: 0.6;
  }
  @media print {
    .no-print {
      display: none;
    }
    .table,
    .summary div {
      border: 0;
      overflow: visible;
    }
    tr,
    .charts > :global(*) {
      break-inside: avoid;
    }
    h2 {
      break-after: avoid;
    }
    .link {
      color: inherit;
    }
  }
</style>

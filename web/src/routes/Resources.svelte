<script lang="ts">
  import AppsTable from '../components/AppsTable.svelte';
  import Chart from '../components/Chart.svelte';
  import { api } from '../lib/api';
  import { CHARTS, CORES, label, query } from '../lib/charts';
  import { append, pick, RANGES, type Columns, type Range } from '../lib/history';
  import { t } from '../lib/i18n';
  import type { Sample } from '../lib/types';
  import type { Live } from '../lib/ws';

  let { live }: { live: Live } = $props();

  const PREFS = 'hearth.resources';
  const REFRESH_MS = 60_000; // downsampled tables change once a minute

  type View = 'charts' | 'apps';

  function loadPrefs(): { range: Range; cores: boolean; view: View } {
    try {
      const p = JSON.parse(localStorage.getItem(PREFS) ?? '{}') as { range?: unknown; cores?: unknown; view?: unknown };
      const range = RANGES.find((r) => r.id === p.range)?.id ?? '10m';
      return { range, cores: p.cores === true, view: p.view === 'apps' ? 'apps' : 'charts' };
    } catch {
      return { range: '10m', cores: false, view: 'charts' };
    }
  }

  const prefs = loadPrefs();
  let range = $state<Range>(prefs.range);
  let cores = $state(prefs.cores);
  let viewMode = $state<View>(prefs.view);
  let cols = $state.raw<Columns | null>(null);
  let failed = $state(false);
  let now = $state(Date.now());
  let gpuNames = $state<Record<string, string>>({});

  $effect(() => {
    try {
      localStorage.setItem(PREFS, JSON.stringify({ range, cores, view: viewMode }));
    } catch {
      // per-viewer convenience only
    }
  });

  // Load the range, then keep it current: 10 min grows with every live
  // sample (fetching only what is new); longer ranges refresh each minute.
  $effect(() => {
    if (viewMode !== 'charts') return;
    const r = range;
    const span = (RANGES.find((x) => x.id === r)?.seconds ?? 600) * 1000;
    let stopped = false;
    let busy = false;
    cols = null;
    failed = false;

    async function load(since?: number): Promise<void> {
      if (busy) return;
      busy = true;
      try {
        const path =
          r === '10m'
            ? `/metrics/live?${query({ since: String(since ?? Date.now() - span) })}`
            : `/metrics/history?${query({ range: r })}`;
        const got = await api<Columns>(path);
        if (stopped) return;
        now = Date.now();
        cols = r === '10m' && cols && since !== undefined ? append(cols, got, now - span) : got;
        failed = false;
      } catch {
        if (!stopped) failed = true;
      } finally {
        busy = false;
      }
    }

    void load();
    const unsub = live.subscribe('metrics', (data) => {
      const s = data as Sample;
      if (s && Array.isArray(s.gpus)) {
        const names: Record<string, string> = {};
        for (const g of s.gpus) names[g.pci] = g.name || g.driver;
        if (JSON.stringify(names) !== JSON.stringify(gpuNames)) gpuNames = names;
      }
      if (r === '10m' && cols) void load(cols.t.at(-1) ?? Date.now() - span);
    });
    const timer = r === '10m' ? undefined : setInterval(() => void load(), REFRESH_MS);
    return () => {
      stopped = true;
      unsub();
      clearInterval(timer);
    };
  });

  const span = $derived(RANGES.find((x) => x.id === range)?.seconds ?? 600);
  const xRange = $derived<[number, number]>([now / 1000 - span, now / 1000]);
  const charts = $derived(
    cols === null
      ? []
      : CHARTS.map((c) => {
          const sel = c.id === 'cpu' && cores ? [...c.sel, CORES] : c.sel;
          const keys = pick(cols as Columns, sel);
          return { spec: c, keys, labels: keys.map((k) => label(k, gpuNames)) };
        }).filter((c) => c.keys.length > 0),
  );
</script>

<header>
  <div>
    <h1>{t('resources.title')}</h1>
    <p>{t('resources.intro')}</p>
  </div>
  <div class="controls">
    <div class="segmented" role="radiogroup" aria-label={t('resources.view')}>
      <button type="button" role="radio" aria-checked={viewMode === 'charts'} class:on={viewMode === 'charts'} onclick={() => (viewMode = 'charts')}>
        {t('resources.charts')}
      </button>
      <button type="button" role="radio" aria-checked={viewMode === 'apps'} class:on={viewMode === 'apps'} onclick={() => (viewMode = 'apps')}>
        {t('resources.apps')}
      </button>
    </div>
    {#if viewMode === 'charts'}
      <label class="check">
      <input type="checkbox" bind:checked={cores} />
      {t('resources.cores')}
    </label>
    <div class="segmented" role="radiogroup" aria-label={t('resources.range')}>
      {#each RANGES as r (r.id)}
        <button type="button" role="radio" aria-checked={range === r.id} class:on={range === r.id} onclick={() => (range = r.id)}>
          {t(r.label)}
        </button>
      {/each}
    </div>
    {/if}
  </div>
</header>

{#if viewMode === 'apps'}
  <AppsTable {live} />
{:else}
  {#if failed}
    <p class="note error" role="status">{t('resources.error')}</p>
  {/if}
  {#if cols === null}
    {#if !failed}<p class="note">{t('resources.loading')}</p>{/if}
  {:else if charts.length === 0}
    <p class="note">{t('resources.empty')}</p>
  {:else}
    <div class="grid">
      {#each charts as c (c.spec.id)}
        <Chart
          title={t(c.spec.title)}
          unit={c.spec.unit}
          keys={c.keys}
          labels={c.labels}
          {cols}
          {xRange}
          min={c.spec.min}
          max={c.spec.max}
        />
      {/each}
    </div>
  {/if}
{/if}

<style>
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    flex-wrap: wrap;
    gap: var(--space-4);
    margin-bottom: var(--space-5);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    flex-wrap: wrap;
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-size: 13px;
    color: var(--text-muted);
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
  .segmented button:hover:not(.on) {
    background: var(--surface-2);
  }
  .segmented button.on {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .note {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
    gap: var(--space-4);
  }
  @media (max-width: 520px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
</style>

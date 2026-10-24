<script lang="ts">
  import Sparkline from '../Sparkline.svelte';
  import Stat from './Stat.svelte';
  import { celsius } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import type { LiveMetrics } from '../../lib/metrics.svelte';
  import type { Sample } from '../../lib/types';

  // The CPU temperature, then the hottest other sensors and the fans.
  const SHOW = 4;

  let { sample, metrics }: { sample: Sample; metrics: LiveMetrics } = $props();
  const hottest = $derived([...(sample.temps ?? [])].sort((a, b) => b.c - a.c).slice(0, SHOW));
  const hot = (c: number, max?: number) => c >= (max && max > 0 ? max : 90);
  const data = $derived(metrics.series(['cpu.temp']));
</script>

{#if sample.cpu_temp_c !== undefined}
  <Stat value={celsius(sample.cpu_temp_c)}>{t('card.temps.cpu')}</Stat>
  <Sparkline {data} min={20} max={100} colors={['--warn']} label={t('card.temps.chart')} />
{/if}
{#if hottest.length === 0}
  <p class="muted">{t('card.temps.none')}</p>
{:else}
  <ul>
    {#each hottest as s (s.chip + '/' + s.label)}
      <li class:hot={hot(s.c, s.max_c)}>
        <span class="label">{s.chip} {s.label}</span><span class="num">{celsius(s.c)}</span>
      </li>
    {/each}
    {#each sample.fans ?? [] as f (f.chip + '/' + f.label)}
      <li>
        <span class="label">{f.chip} {f.label}</span><span class="num">{t('card.temps.rpm', { rpm: Math.round(f.rpm) })}</span>
      </li>
    {/each}
  </ul>
{/if}

<style>
  ul {
    list-style: none;
    margin: var(--space-2) 0 0;
    padding: 0;
    display: grid;
    gap: 2px;
    font-size: 13px;
  }
  li {
    display: flex;
    justify-content: space-between;
    gap: var(--space-2);
    color: var(--text-muted);
  }
  li.hot .num {
    color: var(--crit);
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .num {
    font-variant-numeric: tabular-nums;
    color: var(--text);
  }
  .muted {
    color: var(--text-muted);
    font-size: 13px;
    margin: 0;
  }
</style>

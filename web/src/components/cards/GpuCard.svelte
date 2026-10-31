<script lang="ts">
  import Meter from '../Meter.svelte';
  import Sparkline from '../Sparkline.svelte';
  import Stat from './Stat.svelte';
  import { bytes, celsius, pct } from '../../lib/format';
  import { ltr, t } from '../../lib/i18n';
  import type { LiveMetrics } from '../../lib/metrics.svelte';
  import type { GPU } from '../../lib/types';

  let { gpu, metrics }: { gpu: GPU; metrics: LiveMetrics } = $props();
  const key = $derived(`gpu.util:${gpu.pci}`);
  const data = $derived(metrics.series([key]));
  // Without a utilization figure (i915/xe need the root-only PMU) the clock
  // is the headline instead.
  const mhz = $derived(gpu.freq_mhz !== undefined ? ltr(`${Math.round(gpu.freq_mhz)} MHz`) : '');
  const headline = $derived(gpu.util_pct !== undefined ? pct(gpu.util_pct) : mhz || '–');
  const details = $derived(
    [
      gpu.temp_c !== undefined ? celsius(gpu.temp_c) : '',
      gpu.power_w !== undefined ? `${gpu.power_w.toFixed(0)} W` : '',
      gpu.util_pct !== undefined ? mhz : '',
    ].filter(Boolean),
  );
</script>

<p class="name" title={gpu.name}>{gpu.name || gpu.driver}</p>
{#if gpu.sleeping}
  <p class="muted">{t('card.gpu.sleeping')}</p>
{:else}
  <Stat value={headline}>
    {gpu.util_pct === undefined ? t('card.gpu.noUtil') : details.join(' · ')}
  </Stat>
  {#if gpu.util_pct !== undefined}
    <Sparkline {data} max={100} label={t('card.gpu.chart')} />
  {/if}
  {#if gpu.mem_used !== undefined && gpu.mem_total}
    <div class="vram">
      <span>{t('card.gpu.vram', { used: bytes(gpu.mem_used), total: bytes(gpu.mem_total) })}</span>
      <Meter value={(gpu.mem_used / gpu.mem_total) * 100} label={t('card.gpu.vramMeter')} />
    </div>
  {/if}
{/if}

<style>
  .name {
    margin: 0 0 var(--space-2);
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .muted {
    color: var(--text-muted);
    font-size: 13px;
    margin: 0;
  }
  .vram {
    margin-top: var(--space-2);
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
</style>

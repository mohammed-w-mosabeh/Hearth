<script lang="ts">
  import Meter from '../Meter.svelte';
  import Sparkline from '../Sparkline.svelte';
  import Stat from './Stat.svelte';
  import { bytes, pct } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import type { LiveMetrics } from '../../lib/metrics.svelte';
  import type { Sample } from '../../lib/types';

  let { sample, metrics }: { sample: Sample; metrics: LiveMetrics } = $props();
  const m = $derived(sample.mem);
  const usedPct = $derived(m.total > 0 ? (m.used / m.total) * 100 : 0);
  const swapPct = $derived(m.swap_total > 0 ? (m.swap_used / m.swap_total) * 100 : 0);
  const data = $derived(metrics.series(['mem.used']));
</script>

<Stat value={pct(usedPct)}>{t('card.of', { used: bytes(m.used), total: bytes(m.total) })}</Stat>
<Sparkline {data} max={m.total} label={t('card.mem.chart')} />
<div class="swap">
  {#if m.swap_total > 0}
    <span>{t('card.mem.swap', { used: bytes(m.swap_used), total: bytes(m.swap_total) })}</span>
    <Meter value={swapPct} label={t('card.mem.swapMeter')} />
  {:else}
    <span>{t('card.mem.noSwap')}</span>
  {/if}
</div>

<style>
  .swap {
    margin-top: var(--space-2);
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
</style>

<script lang="ts">
  import Sparkline from '../Sparkline.svelte';
  import Stat from './Stat.svelte';
  import { pct } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import type { LiveMetrics } from '../../lib/metrics.svelte';
  import type { Sample } from '../../lib/types';

  let { sample, metrics }: { sample: Sample; metrics: LiveMetrics } = $props();
  const c = $derived(sample.cpu);
  const freq = $derived.by(() => {
    const f = c.freq_mhz.filter((x) => x > 0);
    return f.length ? f.reduce((a, b) => a + b, 0) / f.length / 1000 : 0;
  });
  const data = $derived(metrics.series(['cpu.usage']));
</script>

<Stat value={sample.partial ? '–' : pct(c.usage)}>
  {t('card.cpu.load', { l1: c.load1.toFixed(2), l5: c.load5.toFixed(2), l15: c.load15.toFixed(2) })}
  {#if freq > 0}· {t('card.cpu.freq', { ghz: freq.toFixed(2) })}{/if}
</Stat>
<Sparkline {data} max={100} label={t('card.cpu.chart')} />

<script lang="ts">
  import Sparkline from '../Sparkline.svelte';
  import Stat from './Stat.svelte';
  import { rate } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import type { LiveMetrics } from '../../lib/metrics.svelte';
  import type { Sample } from '../../lib/types';

  let { sample, metrics }: { sample: Sample; metrics: LiveMetrics } = $props();
  const phys = $derived((sample.net ?? []).filter((n) => !n.virtual));
  const rx = $derived(phys.reduce((a, n) => a + n.rx_bps, 0));
  const tx = $derived(phys.reduce((a, n) => a + n.tx_bps, 0));
  const busiest = $derived([...phys].sort((a, b) => b.rx_bps + b.tx_bps - (a.rx_bps + a.tx_bps))[0]);
  const data = $derived(metrics.series(['net.rx', 'net.tx']));
</script>

<Stat value={sample.partial ? '–' : `↓ ${rate(rx)}`}>
  ↑ {sample.partial ? '–' : rate(tx)}
  {#if busiest}· {busiest.name}{/if}
</Stat>
<Sparkline {data} colors={['--info', '--accent']} label={t('card.net.chart')} />
<p class="legend">
  <span class="rx">{t('card.net.down')}</span>
  <span class="tx">{t('card.net.up')}</span>
</p>

<style>
  .legend {
    display: flex;
    gap: var(--space-3);
    margin: var(--space-1) 0 0;
    font-size: 12px;
    color: var(--text-muted);
  }
  .legend span::before {
    content: '';
    display: inline-block;
    width: 10px;
    height: 2px;
    margin-inline-end: var(--space-1);
    vertical-align: middle;
  }
  .rx::before {
    background: var(--info);
  }
  .tx::before {
    background: var(--accent);
  }
</style>

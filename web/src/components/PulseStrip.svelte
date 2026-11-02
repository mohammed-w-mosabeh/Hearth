<script lang="ts">
  import { t } from '../lib/i18n';
  import { celsius, pct, rate } from '../lib/format';
  import { LiveMetrics } from '../lib/metrics.svelte';
  import { href } from '../lib/router.svelte';
  import type { Live } from '../lib/ws';

  // Prototype of task 8.5. It subscribes like Home; the 15 s slow rate and
  // the jump to a metric come with that task.
  let { live }: { live: Live } = $props();

  let metrics = $state.raw<LiveMetrics | null>(null);
  $effect(() => {
    const m = new LiveMetrics(live);
    metrics = m;
    return () => m.close();
  });

  const s = $derived(metrics?.latest ?? null);
  const hottest = $derived.by(() => {
    const all = [...(s?.temps ?? []).map((x) => x.c), ...(s?.cpu_temp_c === undefined ? [] : [s.cpu_temp_c])];
    return all.length > 0 ? Math.max(...all) : null;
  });
  const down = $derived((s?.net ?? []).filter((n) => !n.virtual).reduce((sum, n) => sum + n.rx_bps, 0));
  const up = $derived((s?.net ?? []).filter((n) => !n.virtual).reduce((sum, n) => sum + n.tx_bps, 0));
  const battery = $derived(s?.power.batteries?.[0] ?? null);
</script>

<a class="pulse no-print" href={href('resources')} aria-label={t('pulse.label')}>
  {#if s}
    <span><b>{t('pulse.cpu')}</b> {pct(s.cpu.usage)}</span>
    <span><b>{t('pulse.ram')}</b> {pct(s.mem.total > 0 ? (s.mem.used / s.mem.total) * 100 : 0)}</span>
    {#if hottest !== null}<span><b>{t('pulse.temp')}</b> {celsius(hottest)}</span>{/if}
    <span><b>{t('pulse.down')}</b> {rate(down)}</span>
    <span><b>{t('pulse.up')}</b> {rate(up)}</span>
    {#if battery}<span><b>{t('pulse.battery')}</b> {pct(battery.pct)}</span>{/if}
  {:else}
    <span>{t('pulse.waiting')}</span>
  {/if}
</a>

<style>
  .pulse {
    position: sticky;
    inset-block-start: calc(-1 * var(--space-5));
    z-index: 2;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1) var(--space-5);
    min-height: var(--pulse-h);
    margin-block: calc(-1 * var(--space-5)) var(--space-4);
    margin-inline: calc(-1 * var(--space-6));
    padding: var(--space-1) var(--space-6);
    background: var(--surface);
    border-block-end: 1px solid var(--border);
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 12px;
    text-decoration: none;
  }
  b {
    color: var(--text-muted);
    font-weight: 400;
  }
</style>

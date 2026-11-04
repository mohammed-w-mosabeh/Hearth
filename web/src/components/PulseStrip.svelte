<script lang="ts">
  import { api } from '../lib/api';
  import { celsius, pct, rate } from '../lib/format';
  import { t } from '../lib/i18n';
  import { PULSE_MS, emptyPulse, pulseChart, pulseFromSample, pulseFromSeries, pulseQuery } from '../lib/pulse';
  import { href } from '../lib/router.svelte';
  import type { LiveSeries, Sample } from '../lib/types';
  import type { Live } from '../lib/ws';

  let { live }: { live: Live } = $props();

  let pulse = $state.raw(emptyPulse);
  let memTotal = $state(0);

  $effect(() => {
    const read = (): void => {
      api<LiveSeries>(`/metrics/live?${pulseQuery(Date.now())}`)
        .then((s) => (pulse = pulseFromSeries(s)))
        .catch(() => {});
    };
    read();
    const timer = setInterval(read, PULSE_MS);
    // One sample for the memory total, which no series holds. Then it only
    // listens, so it never keeps the sampler at 1 s.
    let unsubscribe: (() => void) | null = null;
    const stop = (): void => {
      unsubscribe?.();
      unsubscribe = null;
    };
    unsubscribe = live.subscribe('metrics', (data) => {
      const s = data as Sample;
      if (typeof s?.mem?.total === 'number') {
        memTotal = s.mem.total;
        pulse = pulseFromSample(s);
        setTimeout(stop, 0);
      }
    });
    const quiet = live.listen('metrics', (data) => {
      const s = data as Sample;
      if (typeof s?.t === 'string') pulse = pulseFromSample(s);
    });
    return () => {
      clearInterval(timer);
      stop();
      quiet();
    };
  });

  const ram = $derived(pulse.memUsed !== null && memTotal > 0 ? (pulse.memUsed / memTotal) * 100 : null);
  const link = (chart: string): string => href('resources', chart);
</script>

<div class="pulse no-print" role="group" aria-label={t('pulse.label')}>
  {#if pulse.cpu !== null}<a href={link(pulseChart.cpu)}><b>{t('pulse.cpu')}</b> {pct(pulse.cpu)}</a>{/if}
  {#if ram !== null}<a href={link(pulseChart.ram)}><b>{t('pulse.ram')}</b> {pct(ram)}</a>{/if}
  {#if pulse.temp !== null}<a href={link(pulseChart.temp)}><b>{t('pulse.temp')}</b> {celsius(pulse.temp)}</a>{/if}
  {#if pulse.down !== null}<a href={link(pulseChart.net)}><b>{t('pulse.down')}</b> {rate(pulse.down)}</a>{/if}
  {#if pulse.up !== null}<a href={link(pulseChart.net)}><b>{t('pulse.up')}</b> {rate(pulse.up)}</a>{/if}
  {#if pulse.battery !== null}<a href={link(pulseChart.battery)}><b>{t('pulse.battery')}</b> {pct(pulse.battery)}</a>{/if}
</div>

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
    font-family: var(--font-mono);
    font-size: 12px;
  }
  a {
    color: var(--text);
    text-decoration: none;
  }
  a:hover {
    text-decoration: underline;
  }
  b {
    color: var(--text-muted);
    font-weight: 400;
  }
</style>

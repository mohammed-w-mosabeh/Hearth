<script lang="ts">
  import Sparkline from '../Sparkline.svelte';
  import { t } from '../../lib/i18n';
  import { series } from '../../lib/inspect';

  // A live mini chart: what arrived since the drawer opened.
  let { points, label, value, max }: { points: [number, number][]; label: string; value: string; max?: number } = $props();

  // The width holds what has arrived, so the line fills it from the start.
  const span = $derived(Math.max(4, (points[points.length - 1]?.[0] ?? 0) - (points[0]?.[0] ?? 0)));
</script>

<div class="mini">
  <div class="head"><span class="muted">{label}</span> <span class="mono">{value}</span></div>
  {#if points.length > 1}
    <Sparkline data={series(points)} {label} {span} {...max === undefined ? {} : { max }} />
  {:else}
    <p class="muted wait">{t('inspect.chartWaiting')}</p>
  {/if}
</div>

<style>
  .mini {
    margin-top: var(--space-2);
  }
  .head {
    display: flex;
    justify-content: space-between;
    font-size: 12px;
  }
  .wait {
    font-size: 12px;
    margin: var(--space-1) 0 0;
    min-height: 44px;
  }
</style>

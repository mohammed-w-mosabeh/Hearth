<script lang="ts">
  import Meter from '../Meter.svelte';
  import Sparkline from '../Sparkline.svelte';
  import Stat from './Stat.svelte';
  import { duration, pct } from '../../lib/format';
  import { t, type MessageKey } from '../../lib/i18n';
  import type { LiveMetrics } from '../../lib/metrics.svelte';
  import type { Power } from '../../lib/types';

  let { power, metrics }: { power: Power; metrics: LiveMetrics } = $props();
  const batteries = $derived(power.batteries ?? []);

  const states: Record<string, MessageKey> = {
    charging: 'battery.charging',
    discharging: 'battery.discharging',
    full: 'battery.full',
    empty: 'battery.empty',
    'pending-charge': 'battery.pendingCharge',
    'pending-discharge': 'battery.pendingDischarge',
  };
  const profiles: Record<string, MessageKey> = {
    'power-saver': 'profile.powerSaver',
    balanced: 'profile.balanced',
    performance: 'profile.performance',
  };
  const stateText = (s: string) => t(states[s] ?? 'battery.unknown');
  const profileText = (p: string) => {
    const key = profiles[p];
    return key ? t(key) : p;
  };
  const data = $derived(metrics.series(batteries.slice(0, 1).map((b) => `battery.pct:${b.id}`)));
</script>

{#each batteries as b, i (b.id)}
  <Stat value={pct(b.pct)}>
    {stateText(b.state)}
    {#if b.state === 'discharging' && b.to_empty_s}· {t('card.power.toEmpty', { time: duration(b.to_empty_s) })}{/if}
    {#if b.state === 'charging' && b.to_full_s}· {t('card.power.toFull', { time: duration(b.to_full_s) })}{/if}
    {#if b.rate_w > 0}· {b.rate_w.toFixed(1)} W{/if}
  </Stat>
  {#if i === 0}
    <Sparkline {data} max={100} colors={['--ok']} label={t('card.power.chart')} />
  {:else}
    <Meter value={b.pct} warn={101} crit={101} label={t('card.power.meter', { id: b.id })} />
  {/if}
  {#if b.health_pct > 0}
    <p class="muted">{t('card.power.health', { id: b.id, health: pct(b.health_pct) })}</p>
  {/if}
{/each}
<ul>
  {#if power.on_ac !== undefined}
    <li>{power.on_ac ? t('card.power.ac') : t('card.power.onBattery')}</li>
  {/if}
  {#if power.profile}
    <li>{t('card.power.profile', { name: profileText(power.profile.active) })}</li>
    {#if power.profile.degraded}<li class="warn">{t('card.power.degraded', { reason: power.profile.degraded })}</li>{/if}
  {/if}
</ul>

<style>
  ul {
    list-style: none;
    margin: var(--space-2) 0 0;
    padding: 0;
    font-size: 13px;
    color: var(--text-muted);
  }
  .warn {
    color: var(--warn);
  }
  .muted {
    margin: var(--space-1) 0 0;
    font-size: 12px;
    color: var(--text-muted);
  }
</style>

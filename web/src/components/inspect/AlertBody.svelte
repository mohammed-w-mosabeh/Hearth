<script lang="ts">
  import { api, ApiError } from '../../lib/api';
  import { valueText, type AlertEvent, type Metric } from '../../lib/alerts';
  import { t } from '../../lib/i18n';
  import type { Inspect } from '../../lib/inspect';
  import { href } from '../../lib/router.svelte';

  // One alert event: what crossed, when, and how far.
  let { item }: { item: Extract<Inspect, { kind: 'alert' }> } = $props();

  let e = $state.raw<AlertEvent | null>(null);
  let missing = $state(false);
  let failed = $state('');
  let catalog = $state.raw<Metric[]>([]);
  const metric = $derived(catalog.find((m) => m.id === e?.metric));

  $effect(() => {
    // Events come newest first by id: the one below id + 1 is this one.
    api<{ events: AlertEvent[] }>(`/alerts/events?limit=1&before=${item.id + 1}`)
      .then((r) => {
        e = r.events.find((x) => x.id === item.id) ?? null;
        missing = e === null;
      })
      .catch((err: unknown) => (failed = err instanceof ApiError ? err.message : t('inspect.error')));
    // The catalog gives the value's unit.
    api<{ metrics: Metric[] }>('/alerts/metrics')
      .then((r) => (catalog = r.metrics))
      .catch(() => {});
  });

  const when = (ms: number): string => new Date(ms).toLocaleString();
</script>

<h2 id="inspect-title">{e?.rule_name ?? t('inspect.kind.alert')}</h2>
<p class="sub">{e ? (e.ended_at ? t('inspect.alertEnded') : t('inspect.alertFiring')) : t('inspect.kind.alert')}</p>

{#if failed}
  <p class="error">{failed}</p>
{:else if missing}
  <p class="muted">{t('inspect.gone')}</p>
{:else if e}
  <dl>
    <dt>{t('inspect.severity')}</dt>
    <dd>{e.severity}</dd>
    <dt>{t('inspect.metric')}</dt>
    <dd class="mono">{e.metric}{e.instance ? ` (${e.instance})` : ''}</dd>
    <dt>{t('inspect.rule')}</dt>
    <dd class="mono">{e.op} {valueText(e.threshold, metric)}</dd>
    <dt>{t('inspect.value')}</dt>
    <dd class="mono">{valueText(e.value, metric)}</dd>
    <dt>{t('inspect.peak')}</dt>
    <dd class="mono">{valueText(e.peak, metric)}</dd>
    <dt>{t('inspect.started')}</dt>
    <dd>{when(e.started_at)}</dd>
    {#if e.ended_at}<dt>{t('inspect.ended')}</dt><dd>{when(e.ended_at)}</dd>{/if}
  </dl>

  <h3>{t('inspect.actions')}</h3>
  <div class="actions">
    <a href={href('alerts')}>{t('inspect.fullPage')}</a>
  </div>
{:else}
  <p class="muted">{t('inspect.loading')}</p>
{/if}

<script lang="ts">
  import { reasonKey, run } from '../lib/actions';
  import { api, ApiError } from '../lib/api';
  import { locale, t } from '../lib/i18n';
  import { limitChoices, type SleepReport } from '../lib/sleep';
  import { toast } from '../lib/toast.svelte';

  // Sleep (ROADMAP 6.4). Who and why come from the programs that hold sleep
  // back: plain text, escaped by Svelte.
  const SHOWN = 10;
  let report = $state.raw<SleepReport | null>(null);
  let busy = $state(false);
  let chosen = $state<Record<string, number>>({});

  const load = () =>
    api<SleepReport>('/power/sleep')
      .then((r) => (report = r))
      .catch(() => {});
  $effect(() => void load());

  const when = (ms: number) => new Date(ms).toLocaleString(locale('en-GB'));

  async function setLimit(name: string, end: number): Promise<void> {
    busy = true;
    try {
      const r = await run('battery', 'threshold', name, { end: String(end) });
      const failed = r.failed[0];
      if (failed) toast(t('sleep.limitFailed', { reason: failed.detail ?? t(reasonKey(failed.reason), { detail: '', user: '' }) }), { tone: 'error' });
      else toast(t('sleep.limitSet', { name }));
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    } finally {
      busy = false;
      await load();
    }
  }
</script>

{#if report}
  {@const r = report}
  <div class="sleep">
    <h3>{t('sleep.inhibitors')}</h3>
    {#if r.inhibitors.length === 0}
      <p class="note">{t('sleep.noInhibitors')}</p>
    {:else}
      <ul>
        {#each r.inhibitors as x, i (`${x.pid}-${i}`)}
          <li><strong>{x.who}</strong> <span class="note">{t(x.mode === 'block' ? 'sleep.block' : 'sleep.delay')}</span><span class="why">{x.why}</span></li>
        {/each}
      </ul>
    {/if}
    <h3>{t('sleep.history')}</h3>
    {#if r.limited}<p class="note">{t('sleep.limited')}</p>{/if}
    {#if r.sleeps.length === 0}
      {#if !r.limited}<p class="note">{t('sleep.noHistory')}</p>{/if}
    {:else}
      <ul>
        {#each r.sleeps.slice(0, SHOWN) as s (s.from)}
          <li>{s.to ? t('sleep.stretch', { mode: s.mode, from: when(s.from), to: when(s.to) }) : t('sleep.open', { mode: s.mode, from: when(s.from) })}</li>
        {/each}
      </ul>
    {/if}
    {#each r.limits as l (l.battery)}
      <h3>{t('sleep.limit', { name: l.battery })}</h3>
      <p class="note">{t('sleep.limitHelp')}</p>
      {#if l.via === 'upower'}
        <label><input type="checkbox" checked={l.enabled} disabled={busy} onchange={(e) => setLimit(l.battery, e.currentTarget.checked ? (l.end || 80) : 100)} /> {t('sleep.limitUpower')}{l.end ? ` (${l.end}%)` : ''}</label>
      {:else}
        <div class="limit">
          <select aria-label={t('sleep.limit', { name: l.battery })} value={chosen[l.battery] ?? l.end} onchange={(e) => (chosen[l.battery] = Number(e.currentTarget.value))}>
            {#each limitChoices(l.end) as n (n)}<option value={n}>{n === 100 ? t('sleep.limitOff') : `${n}%`}</option>{/each}
          </select>
          <button type="button" disabled={busy} onclick={() => setLimit(l.battery, chosen[l.battery] ?? l.end ?? 100)}>{t('sleep.limitSave')}</button>
        </div>
      {/if}
    {/each}
  </div>
{/if}

<style>
  h3 {
    font-size: 13px;
    margin: var(--space-3) 0 var(--space-1);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    font-size: 13px;
  }
  li {
    padding: 2px 0;
    overflow-wrap: anywhere;
  }
  .why {
    display: block;
    font-size: 12px;
    color: var(--text-muted);
  }
  .note {
    margin: 0;
    font-size: 12px;
    color: var(--text-muted);
  }
  .limit {
    display: flex;
    gap: var(--space-2);
    margin-top: var(--space-1);
  }
  select,
  button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
</style>

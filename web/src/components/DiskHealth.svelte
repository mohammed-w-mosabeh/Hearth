<script lang="ts">
  import { run, reasonKey } from '../lib/actions';
  import { api, ApiError } from '../lib/api';
  import { bytes, celsius, pct } from '../lib/format';
  import { locale, t } from '../lib/i18n';
  import { days, pollMs, statusKeys, verdict, verdictKeys, worrying, type Health, type SmartDisk } from '../lib/smart';
  import { toast } from '../lib/toast.svelte';

  // Disk health (ROADMAP 5.3). Read once on opening the page, and again
  // while a self-test runs. Model and test texts come from the disk's
  // firmware: plain text, escaped by Svelte.
  let health = $state.raw<Health | null>(null);
  let failed = $state('');
  let busy = $state('');
  let timer: ReturnType<typeof setTimeout> | undefined;
  let gone = false;

  async function load(): Promise<void> {
    clearTimeout(timer);
    try {
      const h = await api<Health>('/disks/health');
      if (gone) return;
      health = h;
      failed = '';
      const ms = pollMs(h.disks);
      if (ms !== null) timer = setTimeout(() => void load(), ms);
    } catch (err) {
      if (!gone) failed = err instanceof ApiError ? err.message : t('action.failed');
    }
  }

  $effect(() => {
    void load();
    return () => {
      gone = true;
      clearTimeout(timer);
    };
  });

  async function start(d: SmartDisk, kind: 'short' | 'long'): Promise<void> {
    busy = d.device;
    try {
      const r = await run('disk', 'selftest', d.device, { kind });
      const bad = r.failed[0];
      if (bad) {
        toast(t('disks.health.startFailed', { name: d.device, reason: bad.detail ?? t(reasonKey(bad.reason), { detail: '', user: '' }) }), { tone: 'error' });
      } else {
        toast(t('disks.health.started', { name: d.device }));
      }
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    } finally {
      busy = '';
    }
  }
</script>

{#if failed}
  <p class="note error" role="status">{failed}</p>
{:else if !health}
  <p class="note">{t('disks.health.loading')}</p>
{:else if health.status !== 'ok'}
  <p class="note">{t(statusKeys[health.status])}</p>
{:else if health.disks.length === 0}
  <p class="note">{t('disks.health.none')}</p>
{:else}
  <ul class="disks">
    {#each health.disks as d (d.device)}
      {@const v = verdict(d)}
      <li class="card" class:failing={v === 'failing'}>
        <div class="head">
          <strong>{d.device}</strong>
          <span class={`state ${v}`}>{t(verdictKeys[v])}</span>
        </div>
        {#if d.model || d.capacity}
          <span class="muted model">{[d.model, d.capacity ? bytes(d.capacity) : ''].filter(Boolean).join(' · ')}</span>
        {/if}
        {#if d.asleep && d.at}<span class="muted">{t('disks.health.asleep')}</span>{/if}
        {#if d.error}<span class="error small">{t('disks.health.error', { error: d.error })}</span>{/if}
        {#if d.supported}
          <dl>
            {#if d.temp_c !== undefined}<dt>{t('disks.health.temp')}</dt><dd>{celsius(d.temp_c)}</dd>{/if}
            {#if d.power_on_hours !== undefined}<dt>{t('disks.health.hours')}</dt><dd>{t('disks.health.days', { n: days(d.power_on_hours).toLocaleString(locale('en-US')) })}</dd>{/if}
            {#if d.life_pct !== undefined}<dt>{t('disks.health.life')}</dt><dd>{pct(d.life_pct)}</dd>{/if}
            {#if d.reallocated !== undefined}<dt>{t('disks.health.reallocated')}</dt><dd class:warn={worrying(d.reallocated)}>{d.reallocated}</dd>{/if}
            {#if d.pending !== undefined}<dt>{t('disks.health.pending')}</dt><dd class:warn={worrying(d.pending)}>{d.pending}</dd>{/if}
            {#if d.media_errors !== undefined}<dt>{t('disks.health.mediaErrors')}</dt><dd class:warn={worrying(d.media_errors)}>{d.media_errors}</dd>{/if}
            <dt>{t('disks.health.lastTest')}</dt>
            <dd class:warn={d.last_test && !d.last_test.passed}>
              {d.last_test ? t('disks.health.testResult', { kind: d.last_test.kind, result: d.last_test.result }) : t('disks.health.noTest')}
            </dd>
          </dl>
          {#if d.testing}
            <span class="small" role="status">{d.remaining_pct !== undefined ? t('disks.health.testing', { n: d.remaining_pct }) : t('disks.health.testingNoPct')}</span>
          {:else}
            <div class="tests">
              <button type="button" disabled={busy === d.device} onclick={() => start(d, 'short')}>{t('disks.health.short')}</button>
              <button type="button" disabled={busy === d.device} onclick={() => start(d, 'long')}>{t('disks.health.long')}</button>
            </div>
          {/if}
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .note {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .small {
    font-size: 13px;
  }
  .muted {
    color: var(--text-muted);
    font-size: 12px;
  }
  .disks {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: var(--space-3);
  }
  .card {
    display: grid;
    align-content: start;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-4);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    min-width: 0;
  }
  .card.failing {
    border-color: var(--crit);
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: var(--space-2);
  }
  .state {
    font-size: 13px;
    color: var(--text-muted);
    text-align: end;
  }
  .state.ok {
    color: var(--ok);
  }
  .state.failing {
    color: var(--crit);
    font-weight: 600;
  }
  .model {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 2px var(--space-3);
    margin: 0;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  dt {
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    text-align: end;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  dd.warn {
    color: var(--warn);
    font-weight: 600;
  }
  .tests {
    display: flex;
    gap: var(--space-2);
  }
  button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>

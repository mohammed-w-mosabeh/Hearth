<script lang="ts">
  import Skeleton from './Skeleton.svelte';
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import { CRASH_DAYS, ownerKey, peakDay, type CrashReport } from '../lib/crashes';
  import { locale, t } from '../lib/i18n';

  // Crashes and out-of-memory kills (ROADMAP 6.3). Names and paths come
  // from the system: plain text, escaped by Svelte.
  const SHOWN = 50;

  let days = $state<number>(30);
  let report = $state.raw<CrashReport | null>(null);
  let failed = $state('');

  $effect(() => {
    const d = days;
    let stopped = false;
    api<CrashReport>(`/crashes?days=${d}`)
      .then((r) => {
        if (stopped) return;
        report = r;
        failed = '';
      })
      .catch((err: unknown) => {
        if (!stopped) failed = failure(err, 'crashes.error');
      });
    return () => {
      stopped = true;
    };
  });

  const peak = $derived(peakDay(report?.days ?? []));
</script>

<section class="crashes" aria-labelledby="crashes-title">
  <div class="head">
    <h2 id="crashes-title">{t('crashes.title')}</h2>
    <select aria-label={t('crashes.title')} bind:value={days}>
      {#each CRASH_DAYS as n (n)}<option value={n}>{t('crashes.days', { n })}</option>{/each}
    </select>
  </div>
  {#if failed}<p class="error" role="status">{failed}</p>{/if}
  {#if report}
    {@const r = report}
    {#if !r.coredump}<p class="note">{t('crashes.noCoredump')}</p>{/if}
    {#if r.limited}<p class="note">{t('crashes.limited')}</p>{/if}
    {#if r.events.length === 0}
      <p class="note">{t('crashes.none')}</p>
    {:else}
      <ul class="bars" aria-label={t('crashes.chart')}>
        {#each r.days as d (d.day)}
          <li title={t('crashes.count', { day: d.day, crash: d.crash, oom: d.oom })}>
            <span class="oom" style:block-size={`${(d.oom / peak) * 100}%`}></span>
            <span class="crash" style:block-size={`${(d.crash / peak) * 100}%`}></span>
            <span class="sr">{t('crashes.count', { day: d.day, crash: d.crash, oom: d.oom })}</span>
          </li>
        {/each}
      </ul>
      <ul class="events">
        {#each r.events.slice(0, SHOWN) as e, i (`${e.t}-${i}`)}
          {@const key = e.owner ? ownerKey(e.owner) : null}
          <li>
            <strong>{e.name}</strong>
            <span class="muted">
              {t(e.kind === 'oom' ? 'crashes.oom' : 'crashes.crash')}{e.signal ? ` (${e.signal})` : ''}
              {#if key && e.owner}· {t(key, { name: e.owner.name })}{/if}
              · {new Date(e.t).toLocaleString(locale('en-GB'))}
            </span>
            {#if e.exe && e.exe !== e.name}<code>{e.exe}</code>{/if}
          </li>
        {/each}
      </ul>
      {#if r.truncated || r.events.length > SHOWN}<p class="note">{t('crashes.truncated')}</p>{/if}
    {/if}
  {:else if !failed}
    <Skeleton label={t('crashes.loading')} />
  {/if}
</section>

<style>
  .crashes {
    margin-bottom: var(--space-5);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-3);
    margin-bottom: var(--space-2);
  }
  h2 {
    font-size: 15px;
  }
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  .note,
  .error {
    margin: var(--space-1) 0;
    font-size: 13px;
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .bars {
    list-style: none;
    display: flex;
    align-items: flex-end;
    gap: 3px;
    block-size: 48px;
    margin: var(--space-2) 0;
    padding: 0;
  }
  .bars li {
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    flex: 0 1 14px;
    block-size: 100%;
    background: var(--surface-2);
    border-radius: 2px;
    overflow: hidden;
  }
  .crash {
    background: var(--warn);
  }
  .oom {
    background: var(--crit);
  }
  .sr {
    position: absolute;
    inline-size: 1px;
    block-size: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  .events {
    list-style: none;
    margin: 0;
    padding: 0;
    font-size: 13px;
  }
  .events li {
    padding: var(--space-1) 0;
    border-bottom: 1px solid var(--border);
    overflow-wrap: anywhere;
  }
  .muted {
    color: var(--text-muted);
    font-size: 12px;
  }
  code {
    display: block;
    font-size: 12px;
    color: var(--text-muted);
  }
</style>

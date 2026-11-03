<script lang="ts">
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import { locale, t } from '../lib/i18n';
  import { lynisPage, lynisPoints, lynisTrend, type LynisFinding, type LynisSchedule, type LynisView } from '../lib/lynis';
  import { toast } from '../lib/toast.svelte';

  // Lynis on the Security page (ROADMAP 7.1). Nothing here runs an audit
  // until the button is pressed or a schedule is chosen.
  let view = $state.raw<LynisView | null>(null);
  let failed = $state('');
  let running = $state(false);
  let saving = $state(false);

  async function load(): Promise<void> {
    try {
      view = await api<LynisView>('/security/lynis');
      failed = '';
    } catch (err) {
      failed = failure(err, 'lynis.error');
    }
  }

  async function run(): Promise<void> {
    running = true;
    try {
      view = await api<LynisView>('/security/lynis/run', { method: 'POST' });
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
      await load();
    } finally {
      running = false;
    }
  }

  async function schedule(every: LynisSchedule): Promise<void> {
    saving = true;
    try {
      view = await api<LynisView>('/security/lynis/schedule', { method: 'PUT', body: JSON.stringify({ every }) });
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
      await load();
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    void load();
  });

  const when = (ms: number) => new Date(ms).toLocaleString(locale());
  const busy = $derived(running || (view?.running ?? false));
  const trend = $derived(view ? lynisTrend(view.history) : null);
  const line = $derived(view ? lynisPoints(view.history, 240, 40) : '');
</script>

{#snippet rows(list: LynisFinding[])}
  <ul class="findings">
    {#each list as f, i (i)}
      {@const page = lynisPage(f.test)}
      <li>
        <span>{f.text}</span>
        {#if f.details}<code>{f.details}</code>{/if}
        {#if f.solution}<span class="muted">{f.solution}</span>{/if}
        <span class="meta">
          {#if f.test}<code>{f.test}</code>{/if}
          {#if page}<a href={page}>{t('lynis.open')}</a>{/if}
        </span>
      </li>
    {/each}
  </ul>
{/snippet}

<section class="card" aria-labelledby="lynis-title">
  <h2 id="lynis-title">{t('lynis.title')}</h2>
  {#if failed}
    <p class="muted" role="status">{failed}</p>
  {:else if view === null}
    <p class="muted">{t('security.loading')}</p>
  {:else if !view.installed}
    <p class="muted">{t('lynis.install')} <code>sudo apt install lynis</code></p>
  {:else}
    <p class="muted">{t('lynis.intro')}</p>
    <div class="row">
      <button type="button" class="btn" onclick={run} disabled={busy} aria-busy={busy}>
        {busy ? t('lynis.running') : view.report ? t('lynis.again') : t('lynis.run')}
      </button>
      <label class="sched">
        {t('lynis.schedule')}
        <select value={view.schedule} onchange={(e) => schedule(e.currentTarget.value as LynisSchedule)} disabled={saving}>
          <option value="off">{t('lynis.schedule.off')}</option>
          <option value="weekly">{t('lynis.schedule.weekly')}</option>
          <option value="monthly">{t('lynis.schedule.monthly')}</option>
        </select>
      </label>
    </div>
    {#if view.error}<p class="muted" role="status">{t('lynis.failed', { s: view.error })}</p>{/if}

    {#if view.report}
      {@const r = view.report}
      <div class="score">
        <strong>{t('lynis.index', { n: r.index })}</strong>
        {#if trend !== null && trend !== 0}
          <span class={trend < 0 ? 'down' : 'up'}>{t(trend < 0 ? 'lynis.fell' : 'lynis.rose', { n: Math.abs(trend) })}</span>
        {/if}
        <span class="muted">{t('lynis.last', { time: when(r.time), version: r.version ?? '' })}</span>
        {#if line}
          <svg viewBox="0 0 240 40" width="240" height="40" role="img" aria-label={t('lynis.history')}>
            <polyline points={line} />
          </svg>
        {/if}
      </div>
      <h3>{t('lynis.warnings', { n: r.warnings.length })}</h3>
      {#if r.warnings.length > 0}{@render rows(r.warnings)}{/if}
      <details>
        <summary>{t('lynis.suggestions', { n: r.suggestions.length })}</summary>
        {@render rows(r.suggestions)}
      </details>
      {#if r.truncated}<p class="muted">{t('lynis.truncated')}</p>{/if}
    {/if}
  {/if}
</section>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-top: var(--space-4);
    min-width: 0;
  }
  h2 {
    font-size: 15px;
    margin: 0;
  }
  h3 {
    font-size: 13px;
    margin: var(--space-3) 0 var(--space-1);
  }
  .muted {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .row,
  .score {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: center;
    margin-top: var(--space-2);
  }
  .score .muted {
    margin: 0;
  }
  .btn,
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
  }
  .btn {
    cursor: pointer;
  }
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .sched {
    display: inline-flex;
    gap: var(--space-2);
    align-items: center;
    font-size: 13px;
  }
  .down {
    color: var(--crit);
  }
  .up {
    color: var(--ok);
  }
  svg {
    max-width: 100%;
    height: 40px;
  }
  polyline {
    fill: none;
    stroke: var(--accent);
    stroke-width: 1.5;
  }
  .findings {
    list-style: none;
    padding: 0;
    margin: 0;
    overflow-wrap: anywhere;
  }
  .findings li {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--space-1) 0;
    border-bottom: 1px solid var(--border);
  }
  .findings li:last-child {
    border-bottom: 0;
  }
  .meta {
    display: flex;
    gap: var(--space-2);
    align-items: baseline;
    font-size: 12px;
  }
  code {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--text-muted);
  }
  details {
    margin-top: var(--space-2);
  }
  summary {
    cursor: pointer;
    font-size: 13px;
    font-weight: 600;
  }
  a {
    color: var(--accent);
  }
</style>

<script lang="ts">
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import DiskHealth from '../components/DiskHealth.svelte';
  import { child, crumbs, daysLeft, exploreRoot, level, usageQuery, type DiskForecast, type Forecasts, type Usage } from '../lib/disks';
  import { bytes, pct, rate } from '../lib/format';
  import { locale, t } from '../lib/i18n';
  import { href } from '../lib/router.svelte';
  import { openDir } from '../lib/shell';
  import type { Sample } from '../lib/types';
  import type { Live } from '../lib/ws';

  // Disks (ROADMAP 1.16): usage per mount and I/O per disk from the metrics
  // topic, and the folder size explorer for folders the user owns.
  let { live }: { live: Live } = $props();

  const POLL_MS = 1000;

  let sample = $state.raw<Sample | null>(null);
  let roots = $state.raw<string[]>([]);
  let path = $state('');
  let usage = $state.raw<Usage | null>(null);
  let error = $state('');

  $effect(() =>
    live.subscribe('metrics', (data) => {
      const s = data as Sample;
      if (s && typeof s.t === 'string') sample = s;
    }),
  );

  $effect(() => {
    api<{ roots: string[] }>('/disks/roots')
      .then((r) => (roots = r.roots))
      .catch(() => {});
  });

  // Only forecasts the data supports arrive here (internal/forecast).
  let forecasts = $state.raw<DiskForecast[]>([]);
  $effect(() => {
    api<Forecasts>('/forecasts')
      .then((r) => (forecasts = r.disks))
      .catch(() => {});
  });
  const forecastOf = (mount: string) => forecasts.find((f) => f.mount === mount);

  const disks = $derived(sample?.disks ?? []);
  const mounts = $derived(disks.map((d) => d.mount));
  const io = $derived((sample?.disk_io ?? []).slice().sort((a, b) => a.name.localeCompare(b.name)));

  // Measuring is a background job on the server: ask, then poll while it runs.
  let token = 0;
  async function open(p: string, fresh = false): Promise<void> {
    const mine = ++token;
    path = p;
    error = '';
    if (usage?.path !== p) usage = null;
    let first = true;
    for (;;) {
      try {
        const u = await api<Usage>(`/disks/usage?${usageQuery(p, fresh && first)}`);
        first = false;
        if (mine !== token) return;
        usage = u;
        if (!u.running) return;
      } catch (err) {
        if (mine !== token) return;
        error = failure(err, 'disks.error');
        return;
      }
      await new Promise((r) => setTimeout(r, POLL_MS));
      if (mine !== token) return;
    }
  }

  // Leaving the page stops polling; the server drops the job soon after.
  $effect(() => () => {
    token++;
  });

  const trail = $derived(usage ? crumbs(usage.path, usage.root) : []);
  const biggest = $derived(usage?.items[0]?.size ?? 0);
</script>

<header>
  <h1>{t('disks.title')}</h1>
  <p>{t('disks.intro')}</p>
</header>

<h2>{t('disks.mounts')}</h2>
{#if !sample}
  <p class="note">{t('disks.loading')}</p>
{:else}
  <ul class="mounts">
    {#each disks as d (d.mount)}
      {@const root = exploreRoot(d.mount, roots, mounts)}
      {@const f = forecastOf(d.mount)}
      <li class="card">
        <div class="head">
          <strong class="mount" title={d.mount}>{d.mount}</strong>
          <span class="muted">{d.device} · {d.fstype}</span>
        </div>
        <div class="bar" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(d.pct)} aria-label={t('disks.usedOf', { mount: d.mount })}>
          <span class={level(d.pct)} style:width={`${Math.min(100, d.pct)}%`}></span>
        </div>
        <div class="foot">
          <span>{t('disks.usage', { used: bytes(d.used), total: bytes(d.total), pct: pct(d.pct) })}</span>
          <span class="muted">{t('disks.free', { free: bytes(d.avail) })}</span>
        </div>
        {#if f}
          {@const n = daysLeft(f)}
          <p class="forecast" class:warn={f.days <= 7} title={t('disks.forecastTitle', { n: f.window_days })}>
            {n === null ? t('disks.forecastSoon') : t('disks.forecast', { n, rate: bytes(f.per_day) })}
          </p>
        {/if}
        {#if root}
          <button type="button" class="explore" onclick={() => open(root)}>{t(root === d.mount ? 'disks.explore' : 'disks.exploreHome')}</button>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<h2>{t('disks.health')}</h2>
<DiskHealth />

{#if io.length > 0}
  <h2>{t('disks.activity')}</h2>
  <div class="table">
    <table>
      <thead>
        <tr><th>{t('disks.col.disk')}</th><th class="num">{t('disks.col.read')}</th><th class="num">{t('disks.col.write')}</th><th class="num">{t('disks.col.busy')}</th></tr>
      </thead>
      <tbody>
        {#each io as d (d.name)}
          <tr><td>{d.name}</td><td class="num">{rate(d.read_bps)}</td><td class="num">{rate(d.write_bps)}</td><td class="num">{pct(d.busy_pct)}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<h2>{t('disks.sizes')}</h2>
{#if !path}
  <p class="note">
    {t('disks.sizesIntro')}
    {#if roots[0]}<button type="button" class="link" onclick={() => open(roots[0] ?? '')}>{t('disks.exploreHome')}</button>{/if}
  </p>
{:else}
  <nav class="crumbs" aria-label={t('disks.location')}>
    {#each trail as c, i (c.path)}
      {#if i > 0}<span class="sep" aria-hidden="true">/</span>{/if}
      {#if i === trail.length - 1}
        <span aria-current="location">{c.name}</span>
      {:else}
        <button type="button" class="link" onclick={() => open(c.path)}>{c.name}</button>
      {/if}
    {/each}
    <a class="here" href={href('terminal', openDir(usage?.path ?? path))}>{t('term.here')}</a>
    <button type="button" class="refresh" onclick={() => open(path, true)} disabled={usage?.running}>{t('disks.refresh')}</button>
  </nav>
  {#if error}<p class="note error" role="status">{error}</p>{/if}
  {#if usage}
    <p class="measure" role="status">
      {#if usage.running}
        {t('disks.measuring', { done: usage.done, total: usage.total, s: Math.round(usage.took_ms / 1000) })}
      {:else}
        {t('disks.total', { size: bytes(usage.size), files: usage.files.toLocaleString(locale('en-US')) })}
        {#if usage.cached}<span class="muted"> · {t('disks.measuredAt', { when: new Date(usage.at).toLocaleTimeString(locale('en-GB')) })}</span>{/if}
      {/if}
      {#if usage.partial}<span class="warn"> · {t('disks.partial')}</span>{/if}
      {#if usage.unreadable > 0}<span class="muted"> · {t('disks.unreadable', { n: usage.unreadable })}</span>{/if}
    </p>
    <div class="table">
      <table>
        <tbody>
          {#each usage.items as e (e.name)}
            <tr>
              <td class="name">
                {#if e.dir && e.nav && !e.pending}
                  <button type="button" class="link" onclick={() => open(child(usage?.path ?? path, e.name))}>{e.name}/</button>
                {:else}
                  <span>{e.name}{e.dir ? '/' : ''}</span>
                {/if}
                {#if e.link}<span class="tag">{t('disks.tag.link')}</span>{/if}
                {#if e.other_fs}<span class="tag">{t('disks.tag.other')}</span>{/if}
              </td>
              <td class="sizebar">
                {#if !e.pending && biggest > 0}<span style:width={`${(e.size / biggest) * 100}%`}></span>{/if}
              </td>
              <td class="num">{e.pending ? t('disks.pending') : e.other_fs ? '–' : bytes(e.size)}</td>
              <td class="num muted">{e.pending || e.other_fs ? '' : t('disks.files', { n: e.files.toLocaleString(locale('en-US')) })}</td>
            </tr>
          {/each}
          {#if usage.rest}
            <tr>
              <td class="name muted">{t('disks.rest')}</td>
              <td></td>
              <td class="num">{bytes(usage.rest.size)}</td>
              <td class="num muted">{t('disks.files', { n: usage.rest.files.toLocaleString(locale('en-US')) })}</td>
            </tr>
          {/if}
        </tbody>
      </table>
    </div>
  {:else if !error}
    <p class="note">{t('disks.starting')}</p>
  {/if}
{/if}
<p class="foot-note">{t('disks.footnote')}</p>

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: var(--space-5) 0 var(--space-2);
  }
  .note,
  .muted {
    color: var(--text-muted);
  }
  .muted {
    font-size: 12px;
  }
  .error {
    color: var(--crit);
  }
  .warn {
    color: var(--warn);
  }
  .mounts {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: var(--space-3);
  }
  .card {
    display: grid;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-4);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    min-width: 0;
  }
  .head {
    display: grid;
    min-width: 0;
  }
  .mount {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bar {
    height: 8px;
    border-radius: 999px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .bar span.warn {
    background: var(--warn);
  }
  .bar span.crit {
    background: var(--crit);
  }
  .foot {
    display: flex;
    justify-content: space-between;
    gap: var(--space-2);
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  .forecast {
    margin: 0;
    font-size: 13px;
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
  .explore {
    justify-self: start;
  }
  button.link {
    border: 0;
    background: none;
    padding: 0;
    color: var(--accent);
    text-align: start;
  }
  button.link:hover {
    text-decoration: underline;
  }
  .table {
    overflow-x: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  th,
  td {
    padding: 0 var(--space-3);
    height: var(--row-h);
    text-align: start;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
  }
  tr:last-child td {
    border-bottom: 0;
  }
  th {
    color: var(--text-muted);
    font-weight: 600;
  }
  .num {
    text-align: end;
  }
  .name {
    max-width: 0;
    width: 45%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sizebar {
    width: 30%;
  }
  .sizebar span {
    display: block;
    height: 6px;
    border-radius: 999px;
    background: var(--accent);
    opacity: 0.7;
  }
  .tag {
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
  }
  .crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1);
    margin-bottom: var(--space-2);
    font-size: 14px;
  }
  .sep {
    color: var(--text-muted);
  }
  .here {
    margin-inline-start: auto;
    font-size: 13px;
    color: var(--accent);
  }
  .refresh {
    margin-inline-start: var(--space-3);
  }
  .measure {
    font-size: 13px;
    margin: 0 0 var(--space-2);
  }
  .foot-note {
    font-size: 12px;
    color: var(--text-muted);
    margin-top: var(--space-4);
  }
</style>

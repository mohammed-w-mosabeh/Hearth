<script lang="ts">
  import Skeleton from '../components/Skeleton.svelte';
  import { failure } from '../lib/errors';
  import LogExplorer from '../components/LogExplorer.svelte';
  import Crashes from '../components/Crashes.svelte';
  import { api } from '../lib/api';
  import { phaseKey, slowLink, spanText, type BootReport } from '../lib/boot';
  import { locale, t } from '../lib/i18n';
  import { entryTime, journalPageQuery, prioClass, type Boot, type JournalEntry, type JournalFilter, type JournalPage } from '../lib/journal';
  import { href } from '../lib/router.svelte';

  type View = 'logs' | 'boot' | 'explorer';

  const PRIORITIES = ['', '3', '4', '6'] as const;
  const WHEN = [
    [0, 'journal.when.any'],
    [1, 'journal.when.hour'],
    [24, 'journal.when.day'],
    [168, 'journal.when.week'],
  ] as const;

  let view = $state<View>('logs');
  let filter = $state<JournalFilter>({ text: '', unit: '', scope: 'system', priority: '4', hours: 24, boot: '' });
  let entries = $state.raw<JournalEntry[] | null>(null);
  let next = $state('');
  let limited = $state(false);
  let error = $state('');
  let loading = $state(false);
  let boots = $state.raw<Boot[]>([]);
  let boot = $state.raw<BootReport | null>(null);
  let bootFailed = $state('');

  // A new search can start before the previous one ends.
  let seq = 0;

  async function load(after = ''): Promise<void> {
    const mine = ++seq;
    loading = true;
    try {
      const page = await api<JournalPage>(`/journal?${journalPageQuery(filter, Date.now(), after)}`);
      if (mine !== seq) return;
      entries = after ? [...(entries ?? []), ...page.entries] : page.entries;
      next = page.next ?? '';
      limited = page.limited ?? false;
      error = '';
    } catch (err) {
      if (mine === seq) error = failure(err, 'journal.error');
    } finally {
      if (mine === seq) loading = false;
    }
  }

  $effect(() => {
    void load();
    api<{ boots: Boot[] }>('/journal/boots').then((r) => (boots = r.boots), () => {});
  });

  $effect(() => {
    if (view !== 'boot' || boot) return;
    api<BootReport>('/boot').then(
      (r) => {
        boot = r;
        bootFailed = '';
      },
      (err: unknown) => (bootFailed = failure(err, 'boot.error')),
    );
  });

  function submit(e: SubmitEvent): void {
    e.preventDefault();
    void load();
  }

  const label = (p: string) => t(p === '' ? 'journal.priority.all' : p === '3' ? 'journal.priority.3' : p === '4' ? 'journal.priority.4' : 'journal.priority.6');
  const bootName = (b: Boot, i: number) => (i === 0 ? t('journal.boot.current') : new Date(b.first).toLocaleString(locale()));
</script>

<header>
  <h1>{t('journal.title')}</h1>
  <p>{t('journal.intro')}</p>
</header>

<Crashes />

<div class="segmented" role="radiogroup" aria-label={t('journal.title')}>
  {#each ['logs', 'explorer', 'boot'] as const as v (v)}
    <button type="button" role="radio" aria-checked={view === v} class:on={view === v} onclick={() => (view = v)}>
      {t(v === 'logs' ? 'journal.view.logs' : v === 'explorer' ? 'journal.view.explorer' : 'journal.view.boot')}
    </button>
  {/each}
</div>

{#if view === 'logs'}
  <form class="filters" onsubmit={submit}>
    <label class="field wide"><span>{t('journal.text')}</span><input type="search" bind:value={filter.text} maxlength="200" placeholder={t('journal.text.placeholder')} /></label>
    <label class="field"><span>{t('journal.unit')}</span><input type="text" bind:value={filter.unit} placeholder={t('journal.unit.placeholder')} /></label>
    <label class="field small"><span>{t('journal.scope')}</span>
      <select bind:value={filter.scope}>
        <option value="system">{t('journal.scope.system')}</option>
        <option value="user">{t('journal.scope.user')}</option>
      </select>
    </label>
    <label class="field"><span>{t('journal.priority')}</span>
      <select bind:value={filter.priority}>
        {#each PRIORITIES as p (p)}<option value={p}>{label(p)}</option>{/each}
      </select>
    </label>
    <label class="field"><span>{t('journal.when')}</span>
      <select bind:value={filter.hours}>
        {#each WHEN as [h, key] (h)}<option value={h}>{t(key)}</option>{/each}
      </select>
    </label>
    <label class="field"><span>{t('journal.boot')}</span>
      <select bind:value={filter.boot}>
        <option value="">{t('journal.boot.any')}</option>
        {#each boots as b, i (b.id)}<option value={b.id}>{bootName(b, i)}</option>{/each}
      </select>
    </label>
    <button type="submit" class="btn" disabled={loading}>{t('journal.apply')}</button>
  </form>

  {#if limited}<p class="note">{t('journal.limited')}</p>{/if}
  {#if error}<p class="note error" role="status">{error}</p>{/if}
  {#if entries === null}
    {#if !error}<Skeleton label={t('journal.loading')} />{/if}
  {:else if entries.length === 0}
    <p class="note">{t('journal.none')}</p>
  {:else}
    <ul class="entries" aria-busy={loading}>
      {#each entries as e (e.cursor)}
        <li class={prioClass(e.prio)}>
          <time>{entryTime(e.t)}</time>
          <span class="ident">{e.ident ?? e.unit ?? ''}</span>
          <span class="msg">{e.msg}</span>
        </li>
      {/each}
    </ul>
    {#if next}<button type="button" class="btn more" onclick={() => load(next)} disabled={loading}>{t('journal.older')}</button>{/if}
  {/if}
{:else if view === 'explorer'}
  <LogExplorer />
{:else if bootFailed}
  <p class="note error" role="status">{bootFailed}</p>
{:else if boot === null}
  <Skeleton label={t('boot.loading')} />
{:else if boot.error}
  <p class="note error" role="status">{boot.error}</p>
{:else if !boot.finished}
  <p class="note">{t('boot.unfinished')}</p>
{:else}
  {@const b = boot}
  <section class="summary">
    <strong>{t('boot.total', { time: spanText(b.total_ms) })}</strong>
    {#if b.target}<span>{t('boot.ready', { target: b.target, time: spanText(b.target_ms ?? 0) })}</span>{/if}
  </section>
  <dl>
    {#each b.phases as p (p.name)}<div><dt>{t(phaseKey(p.name))}</dt><dd>{spanText(p.ms)}</dd></div>{/each}
  </dl>

  <h2>{t('boot.chain.title')}</h2>
  <p class="note">{t('boot.chain.hint')}</p>
  <ol class="chain">
    {#each b.chain as l (l.name)}
      <li class:slow={slowLink(l, b.chain)}>
        <span class="unit">{l.name}</span>
        <span class="n">{l.ms ? `+${spanText(l.ms)}` : ''}</span>
      </li>
    {/each}
  </ol>

  <h2>{t('boot.blame.title')}</h2>
  <p class="note">{t('boot.blame.hint')}</p>
  <table>
    <tbody>
      {#each b.blame as u (u.name)}
        <tr>
          <td class="unit"><a href={href('services', u.name)} title={t('boot.open')}>{u.name}</a></td>
          <td class="n">{spanText(u.ms)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  h2 {
    font-size: 15px;
    margin: var(--space-4) 0 0;
  }
  header p,
  .note {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .note {
    font-size: 13px;
  }
  .error {
    color: var(--crit);
  }
  .segmented {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow: hidden;
    margin-bottom: var(--space-3);
  }
  .segmented button {
    font: inherit;
    font-size: 13px;
    color: var(--text-muted);
    background: var(--surface);
    border: 0;
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .segmented button.on {
    color: var(--text);
    background: var(--surface-2);
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: flex-end;
    margin-bottom: var(--space-3);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
    color: var(--text-muted);
    flex: 1 1 130px;
    min-width: 0;
  }
  .field.wide {
    flex: 2 1 220px;
  }
  .field.small {
    flex: 0 1 90px;
  }
  input,
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
    min-width: 0;
  }
  .btn {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    white-space: nowrap;
  }
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .more {
    margin-top: var(--space-3);
  }
  .entries {
    list-style: none;
    padding: 0;
    margin: 0;
    font-family: var(--font-mono);
    font-size: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
  }
  .entries li {
    display: grid;
    grid-template-columns: 10em minmax(6em, 12em) minmax(0, 1fr);
    gap: var(--space-2);
    padding: 2px var(--space-2);
    border-bottom: 1px solid var(--border);
  }
  @media (max-width: 720px) {
    .entries li {
      grid-template-columns: 1fr;
      gap: 0;
    }
  }
  .entries time,
  .ident {
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }
  .msg {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    min-width: 0;
  }
  li.crit .msg {
    color: var(--crit);
  }
  li.warn .msg {
    color: var(--warn);
  }
  .summary {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: baseline;
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    background: var(--surface);
    margin-bottom: var(--space-3);
  }
  .summary span {
    color: var(--text-muted);
  }
  dl {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
    gap: var(--space-2);
    margin: 0;
  }
  dt {
    font-size: 12px;
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
  }
  .chain {
    list-style: none;
    padding: 0;
    margin: var(--space-2) 0 0;
    font-size: 13px;
  }
  .chain li {
    display: flex;
    justify-content: space-between;
    gap: var(--space-3);
    padding: 2px var(--space-2);
    border-inline-start: 2px solid var(--border);
  }
  .chain li.slow {
    border-inline-start-color: var(--warn);
    font-weight: 600;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    margin-top: var(--space-2);
  }
  td {
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border);
  }
  .unit {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
    min-width: 0;
  }
  a {
    color: var(--accent);
  }
  .n {
    text-align: end;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
</style>

<script lang="ts">
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import { locale, t } from '../lib/i18n';
  import { added, logsQuery, sourceName, type LogSource } from '../lib/logs';

  // Log explorer (ROADMAP 6.6). The server reads, searches and folds; the
  // lines are what programs logged: plain text, escaped by Svelte.
  const LIVE_MS = 5000;

  // initial preselects sources, as the Apps page does with an app's containers.
  let { initial = [] }: { initial?: string[] } = $props();
  // svelte-ignore state_referenced_locally (a starting choice; the viewer changes it)
  let sources = $state<string[]>([...initial]);
  let containers = $state.raw<string[]>([]);
  let pick = $state('');
  let unit = $state('');
  let pattern = $state('');
  let applied = $state('');
  let live = $state(false);
  let columns = $state.raw<LogSource[]>([]);
  let error = $state('');
  let seq = 0;

  $effect(() => {
    api<{ containers?: { name: string }[] }>('/docker')
      .then((d) => (containers = (d.containers ?? []).map((c) => c.name).sort()))
      .catch(() => {});
  });

  async function load(): Promise<void> {
    const mine = ++seq;
    if (sources.length === 0) {
      columns = [];
      return;
    }
    try {
      const r = await api<{ sources: LogSource[] }>(`/logs?${logsQuery(sources, applied)}`);
      if (mine !== seq) return;
      columns = r.sources;
      error = '';
    } catch (err) {
      if (mine === seq) error = failure(err, 'journal.error');
    }
  }

  $effect(() => {
    void sources.length;
    void applied;
    void load();
    if (!live) return;
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') void load();
    }, LIVE_MS);
    return () => clearInterval(timer);
  });

  function add(id: string): void {
    sources = added(sources, id);
    pick = '';
    unit = '';
  }

  const remove = (id: string) => (sources = sources.filter((s) => s !== id));
  const time = (ms?: number) => (ms ? new Date(ms).toLocaleTimeString(locale('en-GB')) : '');
</script>

<div class="explorer">
  <p class="note">{t('logs.intro')}</p>
  <div class="controls">
    <label>{t('logs.container')}
      <select bind:value={pick} onchange={() => add(pick ? `container:${pick}` : '')}>
        <option value=""></option>
        {#each containers as c (c)}<option value={c}>{c}</option>{/each}
      </select>
    </label>
    <form onsubmit={(e) => { e.preventDefault(); add(unit.trim()); }}>
      <label>{t('logs.unit')}<input type="text" spellcheck="false" bind:value={unit} /></label>
      <button type="submit">{t('logs.add')}</button>
    </form>
    <form onsubmit={(e) => { e.preventDefault(); applied = pattern; }}>
      <label>{t('logs.search')}<input type="search" spellcheck="false" maxlength="200" bind:value={pattern} /></label>
    </form>
    <label class="check"><input type="checkbox" bind:checked={live} /> {t('logs.live')}</label>
  </div>
  {#if error}<p class="error" role="status">{error}</p>{/if}
  {#if sources.length === 0}
    <p class="note">{t('logs.none')}</p>
  {:else}
    <div class="columns" style:--n={columns.length || 1}>
      {#each columns as c (c.id)}
        <section aria-label={sourceName(c.id)}>
          <div class="title">
            <strong>{sourceName(c.id)}</strong>
            <button type="button" aria-label={t('logs.remove', { name: sourceName(c.id) })} onclick={() => remove(c.id)}>×</button>
          </div>
          {#if c.error}
            <p class="error">{c.error}</p>
          {:else if c.lines.length === 0}
            <p class="note">{t('logs.empty')}</p>
          {:else}
            <ol class="lines">
              {#each c.lines as l, i (i)}
                <li class={l.level}>
                  {#if l.t}<time>{time(l.t)}</time>{/if}
                  {#if l.level === 'error' || l.level === 'warn'}<span class="tag">{t(l.level === 'error' ? 'logs.level.error' : 'logs.level.warn')}</span>{/if}
                  <span>{l.text}</span>
                  {#if l.count}<span class="tag">{t('logs.times', { n: l.count })}</span>{/if}
                </li>
              {/each}
            </ol>
          {/if}
          <p class="note">{t('logs.scanned', { n: c.read })}{c.truncated ? ` ${t('logs.truncated')}` : ''}</p>
        </section>
      {/each}
    </div>
  {/if}
</div>

<style>
  .note {
    margin: var(--space-1) 0;
    font-size: 12px;
    color: var(--text-muted);
  }
  .error {
    font-size: 13px;
    color: var(--crit);
  }
  .controls,
  form {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: var(--space-3);
    margin-bottom: var(--space-2);
    font-size: 13px;
  }
  form {
    margin: 0;
    gap: var(--space-2);
  }
  label {
    display: grid;
    gap: 2px;
  }
  label.check {
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }
  input:not([type='checkbox']),
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
  .columns {
    display: grid;
    grid-template-columns: repeat(var(--n), minmax(0, 1fr));
    gap: var(--space-3);
  }
  @media (max-width: 800px) {
    .columns {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  section {
    min-width: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-2);
  }
  .title {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-2);
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .title button {
    border: 0;
    background: none;
    cursor: pointer;
  }
  .lines {
    list-style: none;
    margin: var(--space-1) 0;
    padding: 0;
    max-block-size: 480px;
    overflow-y: auto;
    font-family: var(--font-mono, monospace);
    font-size: 12px;
  }
  .lines li {
    padding: 1px 0;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }
  .lines .error {
    color: var(--crit);
    font-size: inherit;
  }
  .lines .warn {
    color: var(--warn);
  }
  .lines .debug {
    color: var(--text-muted);
  }
  time {
    color: var(--text-muted);
    margin-inline-end: var(--space-1);
  }
  .tag {
    font-size: 11px;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 4px;
    margin-inline: 2px;
  }
</style>

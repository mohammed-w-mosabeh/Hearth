<script lang="ts">
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import { t } from '../lib/i18n';
  import { appendEntries, entryTime, journalQuery, matches, type Journal, type JournalEntry } from '../lib/journal';
  import type { Unit } from '../lib/services';

  // A unit's journal: the last entries, then new ones every few seconds
  // while the dialog is open. Messages are shown as text only.
  let { unit, onclose }: { unit: Unit | null; onclose: () => void } = $props();

  const POLL_MS = 5000;
  const FIRST = 300;

  let dialog: HTMLDialogElement;
  let list = $state<HTMLElement | null>(null);
  let entries = $state.raw<JournalEntry[]>([]);
  let limited = $state(false);
  let failed = $state('');
  let loading = $state(false);
  let filter = $state('');
  let errorsOnly = $state(false);
  let follow = $state(true);

  $effect(() => {
    if (!unit) {
      if (dialog.open) dialog.close();
      return;
    }
    const target = `${unit.scope}/${unit.name}`;
    entries = [];
    limited = false;
    failed = '';
    filter = '';
    errorsOnly = false;
    follow = true;
    if (!dialog.open) dialog.showModal();

    let cursor = '';
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async (): Promise<void> => {
      loading = cursor === '';
      try {
        const j = await api<Journal>(`/services/journal?${journalQuery(target, FIRST, cursor)}`);
        if (stopped) return;
        entries = appendEntries(entries, j.entries);
        cursor = j.cursor ?? cursor;
        limited = j.limited ?? false;
        failed = '';
      } catch (err) {
        if (!stopped) failed = failure(err, 'journal.error');
      } finally {
        loading = false;
      }
      if (!stopped) {
        timer = setTimeout(() => {
          if (document.visibilityState === 'visible') void load();
          else timer = setTimeout(() => void load(), POLL_MS);
        }, POLL_MS);
      }
    };
    void load();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  });

  const shown = $derived(entries.filter((e) => matches(e, filter, errorsOnly)));

  // Keep the newest entry in view unless the user scrolled up.
  $effect(() => {
    void shown.length;
    if (follow && list) list.scrollTop = list.scrollHeight;
  });

  function onscroll(): void {
    if (list) follow = list.scrollHeight - list.scrollTop - list.clientHeight < 24;
  }

  const prioClass = (p: number) => (p <= 3 ? 'err' : p === 4 ? 'warn' : p >= 7 ? 'debug' : '');
</script>

<dialog bind:this={dialog} aria-labelledby="journal-title" onclose={() => unit && onclose()}>
  {#if unit}
    <header>
      <h2 id="journal-title">{t('journal.unitTitle', { name: unit.name })}</h2>
      <button type="button" class="x" aria-label={t('journal.close')} onclick={() => onclose()}>×</button>
    </header>
    <div class="bar">
      <input type="search" placeholder={t('journal.filter')} aria-label={t('journal.filter')} bind:value={filter} />
      <label><input type="checkbox" bind:checked={errorsOnly} /> {t('journal.errorsOnly')}</label>
    </div>
    {#if limited}<p class="note">{t('journal.limited')}</p>{/if}
    {#if failed}<p class="note error" role="status">{failed}</p>{/if}
    {#if loading}
      <p class="note">{t('journal.loading')}</p>
    {:else if shown.length === 0}
      <p class="note">{entries.length === 0 ? t('journal.empty') : t('journal.noMatch')}</p>
    {/if}
    <ol bind:this={list} {onscroll} aria-live="polite">
      {#each shown as e (e.cursor)}
        <li class={prioClass(e.prio)}>
          <time datetime={new Date(e.t).toISOString()}>{entryTime(e.t)}</time>
          <span class="ident">{e.ident ?? ''}{e.pid ? `[${e.pid}]` : ''}</span>
          <span class="msg">{e.msg}</span>
        </li>
      {/each}
    </ol>
    <p class="foot">{t('journal.foot', { n: entries.length })}</p>
  {/if}
</dialog>

<style>
  dialog {
    width: min(1000px, calc(100vw - 32px));
    height: min(720px, calc(100vh - 32px));
    padding: var(--space-4);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog[open] {
    display: flex;
    flex-direction: column;
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: var(--space-2);
  }
  h2 {
    font-size: 17px;
  }
  .x {
    font: inherit;
    font-size: 20px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 0;
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    align-items: center;
    margin-bottom: var(--space-2);
    font-size: 13px;
  }
  input[type='search'] {
    font: inherit;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    min-width: 260px;
  }
  .note,
  .foot {
    color: var(--text-muted);
    font-size: 13px;
    margin: 0 0 var(--space-2);
  }
  .error {
    color: var(--crit);
  }
  .foot {
    margin: var(--space-2) 0 0;
    font-size: 12px;
  }
  ol {
    flex: 1;
    min-height: 0;
    overflow: auto;
    list-style: none;
    margin: 0;
    padding: var(--space-2);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
  }
  li {
    display: grid;
    grid-template-columns: max-content max-content 1fr;
    gap: var(--space-3);
  }
  time,
  .ident {
    color: var(--text-muted);
    white-space: nowrap;
  }
  .msg {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  li.err .msg {
    color: var(--crit);
  }
  li.warn .msg {
    color: var(--warn);
  }
  li.debug {
    opacity: 0.7;
  }
</style>

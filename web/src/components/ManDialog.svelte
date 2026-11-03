<script lang="ts">
  import { failure } from '../lib/errors';
  import { api, ApiError } from '../lib/api';
  import { t } from '../lib/i18n';

  // One tool's man page, as plain text (man removes the formatting on
  // the server). Shown as text only.
  let { name, onclose }: { name: string | null; onclose: () => void } = $props();

  let dialog: HTMLDialogElement;
  let text = $state('');
  let failed = $state('');
  let filter = $state('');

  $effect(() => {
    if (!name) {
      if (dialog.open) dialog.close();
      return;
    }
    text = '';
    failed = '';
    filter = '';
    if (!dialog.open) dialog.showModal();
    let stopped = false;
    api<{ text: string }>(`/tools/man?name=${encodeURIComponent(name)}`)
      .then((r) => {
        if (!stopped) text = r.text;
      })
      .catch((err: unknown) => {
        if (!stopped) failed = err instanceof ApiError && err.status === 404 ? t('tools.man.none') : failure(err, 'tools.man.error');
      });
    return () => {
      stopped = true;
    };
  });

  // Search highlights nothing; it keeps the lines that match, with
  // their section headings.
  const shown = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return text;
    return text
      .split('\n')
      .filter((l) => /^\S/.test(l) || l.toLowerCase().includes(q))
      .join('\n');
  });
</script>

<dialog bind:this={dialog} aria-labelledby="man-title" onclose={() => name && onclose()}>
  {#if name}
    <header>
      <h2 id="man-title">{t('tools.man.title', { name })}</h2>
      <button type="button" class="x" aria-label={t('tools.man.close')} onclick={() => onclose()}>×</button>
    </header>
    <input type="search" placeholder={t('tools.man.filter')} aria-label={t('tools.man.filter')} bind:value={filter} />
    {#if failed}
      <p class="note">{failed}</p>
    {:else if !text}
      <p class="note">{t('tools.man.loading')}</p>
    {:else}
      <pre>{shown}</pre>
    {/if}
  {/if}
</dialog>

<style>
  dialog {
    width: min(900px, calc(100vw - 32px));
    height: min(760px, calc(100vh - 32px));
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
  input[type='search'] {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    max-width: 320px;
    margin-bottom: var(--space-2);
  }
  .note {
    color: var(--text-muted);
    font-size: 13px;
  }
  pre {
    flex: 1;
    min-height: 0;
    overflow: auto;
    margin: 0;
    padding: var(--space-3);
    font-family: var(--font-mono, monospace);
    font-size: 12px;
    line-height: 1.45;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    white-space: pre-wrap;
  }
</style>

<script lang="ts">
  import { api, ApiError } from '../lib/api';
  import { bytes } from '../lib/format';
  import { t } from '../lib/i18n';
  import { pkgReasonKey, type DebDetail, type Row } from '../lib/packages';

  // One package in detail. For apt: dpkg's fields, dependencies, the
  // installed packages that depend on it, and its files. For the other
  // managers: what the listing already holds. Everything is shown as text.
  let { row, onclose, onsearch }: { row: Row | null; onclose: () => void; onsearch: (name: string) => void } = $props();

  const FILES = 200;

  let dialog: HTMLDialogElement;
  let detail = $state.raw<DebDetail | null>(null);
  let failed = $state('');
  let allFiles = $state(false);

  $effect(() => {
    if (!row) {
      if (dialog.open) dialog.close();
      return;
    }
    detail = null;
    failed = '';
    allFiles = false;
    if (!dialog.open) dialog.showModal();
    if (row.manager !== 'apt') return;
    let stopped = false;
    const id = row.key.slice('apt/'.length);
    api<DebDetail>(`/pkgs/apt/${encodeURIComponent(id)}`)
      .then((d) => {
        if (!stopped) detail = d;
      })
      .catch((err: unknown) => {
        if (!stopped) failed = err instanceof ApiError ? err.message : t('pkg.detail.error');
      });
    return () => {
      stopped = true;
    };
  });

  const files = $derived(detail ? (allFiles ? detail.files : detail.files.slice(0, FILES)) : []);

  function search(name: string): void {
    onclose();
    onsearch(name);
  }
</script>

<dialog bind:this={dialog} aria-labelledby="pkg-title" onclose={() => row && onclose()}>
  {#if row}
    <header>
      <h2 id="pkg-title">{row.name}</h2>
      <button type="button" class="x" aria-label={t('pkg.detail.close')} onclick={() => onclose()}>×</button>
    </header>
    {#if row.summary}<p class="summary">{row.summary}</p>{/if}
    <dl>
      <dt>{t('pkg.col.manager')}</dt>
      <dd>{t(`pkg.mgr.${row.manager}`)}</dd>
      <dt>{t('pkg.col.version')}</dt>
      <dd>
        {row.version || '–'}
        {#if row.update}→ {row.update.version}
          {#if row.update.security}<span class="tag sec">{t('pkg.tag.security')}</span>{/if}
        {/if}
      </dd>
      {#if row.size >= 0}
        <dt>{t('pkg.col.size')}</dt>
        <dd>{bytes(row.size)}</dd>
      {/if}
      {#if row.manager === 'apt'}
        <dt>{t('pkg.detail.install')}</dt>
        <dd>{row.auto ? t('pkg.detail.auto') : t('pkg.detail.manual')}{row.held ? ` · ${t('pkg.tag.held')}` : ''}</dd>
        {#if detail?.section}<dt>{t('pkg.detail.section')}</dt><dd>{detail.section}</dd>{/if}
        {#if detail?.priority}<dt>{t('pkg.detail.priority')}</dt><dd>{detail.priority}</dd>{/if}
        {#if detail?.source}<dt>{t('pkg.detail.source')}</dt><dd>{detail.source}</dd>{/if}
        {#if detail?.maintainer}<dt>{t('pkg.detail.maintainer')}</dt><dd>{detail.maintainer}</dd>{/if}
      {:else if row.location}
        <dt>{t('pkg.detail.location')}</dt>
        <dd class="mono">{row.location}</dd>
      {/if}
      {#if !row.removable && row.why}
        <dt>{t('pkg.detail.removal')}</dt>
        <dd>{t(pkgReasonKey(row.why), { detail: row.usedBy?.join(', ') ?? '', user: '' })}</dd>
      {/if}
      {#if row.usedBy?.length}
        <dt>{t('pkg.detail.usedBy')}</dt>
        <dd>{row.usedBy.join(', ')}</dd>
      {/if}
    </dl>

    {#if row.manager === 'apt'}
      {#if failed}
        <p class="note error" role="status">{failed}</p>
      {:else if !detail}
        <p class="note">{t('pkg.detail.loading')}</p>
      {:else}
        <section>
          <h3>{t('pkg.detail.deps', { n: detail.deps.length })}</h3>
          {#if detail.deps.length === 0}<p class="note">{t('pkg.detail.none')}</p>{/if}
          <ul class="chips">
            {#each detail.deps as d (d)}<li><button type="button" onclick={() => search(d)}>{d}</button></li>{/each}
          </ul>
        </section>
        <section>
          <h3>{t('pkg.detail.rdeps', { n: detail.reverse_deps.length })}</h3>
          {#if detail.reverse_deps.length === 0}<p class="note">{t('pkg.detail.noRdeps')}</p>{/if}
          <ul class="chips">
            {#each detail.reverse_deps as d (d)}<li><button type="button" onclick={() => search(d)}>{d}</button></li>{/each}
          </ul>
        </section>
        <section class="files">
          <h3>{t('pkg.detail.files', { n: detail.files.length })}{detail.files_truncated ? '+' : ''}</h3>
          <ol>
            {#each files as f (f)}<li>{f}</li>{/each}
          </ol>
          {#if !allFiles && detail.files.length > FILES}
            <button type="button" class="more" onclick={() => (allFiles = true)}>
              {t('pkg.detail.allFiles', { n: detail.files.length - FILES })}
            </button>
          {/if}
        </section>
      {/if}
    {/if}
  {/if}
</dialog>

<style>
  dialog {
    width: min(760px, calc(100vw - 32px));
    max-height: min(760px, calc(100vh - 32px));
    padding: var(--space-4);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h2 {
    font-size: 17px;
    overflow-wrap: anywhere;
  }
  h3 {
    font-size: 13px;
    font-weight: 600;
    margin: var(--space-4) 0 var(--space-2);
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
  .summary,
  .note {
    color: var(--text-muted);
    font-size: 13px;
  }
  .error {
    color: var(--crit);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--space-1) var(--space-4);
    font-size: 13px;
    margin: var(--space-3) 0 0;
  }
  dt {
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .mono,
  .files ol {
    font-family: var(--font-mono, monospace);
    font-size: 12px;
  }
  .tag {
    font-size: 11px;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
  }
  .tag.sec {
    color: var(--crit);
    border-color: var(--crit);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .chips button {
    font: inherit;
    font-size: 12px;
    color: var(--text);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 var(--space-2);
    cursor: pointer;
  }
  .files ol {
    list-style: none;
    padding: var(--space-2);
    margin: 0;
    max-height: 240px;
    overflow: auto;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow-wrap: anywhere;
  }
  .more {
    font: inherit;
    font-size: 12px;
    color: var(--accent);
    background: none;
    border: 0;
    padding: var(--space-1) 0;
    cursor: pointer;
  }
</style>

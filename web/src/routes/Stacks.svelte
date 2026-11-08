<script lang="ts">
  import ComposeProjects from '../components/ComposeProjects.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import ImportDryRun from '../components/ImportDryRun.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import StackDetail from '../components/StackDetail.svelte';
  import { api } from '../lib/api';
  import type { Snapshot } from '../lib/docker';
  import { failure } from '../lib/errors';
  import { t } from '../lib/i18n';
  import { openInspect } from '../lib/router.svelte';
  import { addresses, detected, exposure, serviceCount, shape, shapes, type StackInfo } from '../lib/stacks';

  // Apps (ROADMAP 9.9): the stacks Hearth manages, and the Compose
  // projects it only detects, which keep today's actions; importing one is
  // optional and starts with its dry-run. This page replaces the Docker
  // page's Compose tab.
  const REFRESH_MS = 15_000;
  const shapeLabel = { ok: 'stacks.state.ok', degraded: 'stacks.state.degraded', warning: 'stacks.state.warning', stopped: 'stacks.state.stopped' } as const;

  let stacks = $state.raw<StackInfo[] | null>(null);
  let snap = $state.raw<Snapshot | null>(null);
  let error = $state('');
  let openName = $state('');
  let importing = $state('');
  let dialog: HTMLDialogElement;

  const open = $derived(stacks?.find((s) => s.name === openName) ?? null);
  const others = $derived(snap?.state === 'ok' && stacks ? detected(snap.projects, stacks) : []);

  async function load(): Promise<void> {
    try {
      const [s, d] = await Promise.all([api<StackInfo[]>('/stacks'), api<Snapshot>('/docker')]);
      stacks = s;
      snap = d;
      error = '';
    } catch (err) {
      error = failure(err, 'stacks.error');
    }
  }

  $effect(() => {
    void load();
    const id = setInterval(() => void load(), REFRESH_MS);
    return () => clearInterval(id);
  });

  $effect(() => {
    if (open && !dialog.open) dialog.showModal();
    else if (!open && dialog.open) dialog.close();
  });

  function show(name: string): void {
    openName = name;
  }
</script>

<section class="page">
  <header>
    <h1>{t('stacks.title')}</h1>
    <p>{t('stacks.intro')}</p>
  </header>

  {#if importing}
    <section class="importing">
      <ImportDryRun
        project={importing}
        onclose={() => (importing = '')}
        onimported={() => {
          importing = '';
          void load();
        }}
      />
    </section>
  {/if}

  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}

  <section class="card">
    <h2>{t('stacks.managed')}</h2>
    {#if stacks === null}
      {#if !error}<Skeleton label={t('stacks.loading')} />{/if}
    {:else if stacks.length === 0}
      <EmptyState inline icon="layers" body={t('stacks.none')} />
    {:else}
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th>{t('stacks.col.state')}</th>
              <th>{t('stacks.col.app')}</th>
              <th>{t('stacks.col.services')}</th>
              <th>{t('stacks.col.address')}</th>
              <th>{t('stacks.col.attention')}</th>
            </tr>
          </thead>
          <tbody>
            {#each stacks as s (s.name)}
              {@const sh = shape(s)}
              {@const ex = exposure(s)}
              <tr>
                <td class="tone-{sh}"><span aria-hidden="true">{shapes[sh]}</span> {t(shapeLabel[sh])}</td>
                <td><button type="button" class="link" onclick={() => show(s.name)}>{s.name}</button></td>
                <td class="mono">{serviceCount(s)}</td>
                <td class="mono">{addresses(s).join(', ') || '–'}</td>
                <td>
                  <span class="chips">
                    {#if ex.exposed.length > 0}<span class="chip warn"><span aria-hidden="true">▲</span> {t('stacks.chipExposed')}</span>{/if}
                    {#each ex.danger as f (f)}<span class="chip crit"><span aria-hidden="true">▲</span> {f}</span>{/each}
                    {#if s.secrets.some((x) => !x.set)}<span class="chip warn">{t('stacks.chipUnset')}</span>{/if}
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section class="card">
    <h2>{t('stacks.detected')}</h2>
    <p class="muted">{t('stacks.detectedIntro')}</p>
    {#if snap === null}
      {#if !error}<Skeleton label={t('stacks.loading')} />{/if}
    {:else if snap.state !== 'ok'}
      <p class="muted">{t('stacks.noDocker')}</p>
    {:else}
      <ComposeProjects projects={others} containers={snap.containers} onopen={(c) => openInspect(`container:${c.id}`)} onimport={(p) => (importing = p.name)} />
    {/if}
  </section>
</section>

<dialog bind:this={dialog} class="drawer" aria-labelledby="stack-title" onclose={() => (openName = '')}>
  {#if open}
    <div class="drawer-head">
      <h2 id="stack-title">{open.name}</h2>
      <button type="button" class="plain" onclick={() => (openName = '')}>{t('stacks.close')}</button>
    </div>
    <StackDetail stack={open} onchanged={() => void load()} />
  {/if}
</dialog>

<style>
  .page {
    display: grid;
    gap: var(--space-4);
  }
  header h1 {
    margin: 0;
  }
  header p {
    margin: var(--space-1) 0 0;
    color: var(--text-muted);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    padding: var(--space-3) var(--space-4);
    min-width: 0;
  }
  .card h2 {
    font-size: 14px;
    margin: 0 0 var(--space-3);
  }
  .importing {
    border: 1px solid var(--accent);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
  }
  .scroll {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th {
    text-align: start;
    font-weight: 500;
    color: var(--text-muted);
    padding: 4px var(--space-3) 4px 0;
  }
  td {
    padding: 6px var(--space-3) 6px 0;
    border-top: 1px solid var(--border);
    white-space: nowrap;
  }
  .chips {
    display: inline-flex;
    flex-wrap: wrap;
    gap: var(--space-1);
  }
  .chip {
    font-size: 12px;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 var(--space-2);
  }
  .chip.crit {
    border-color: var(--crit);
  }
  .chip.warn {
    border-color: var(--warn);
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .muted {
    color: var(--text-muted);
    font-size: 13px;
  }
  .error {
    border: 1px solid var(--crit);
    border-radius: var(--radius-s);
    padding: var(--space-2) var(--space-3);
  }
  .link {
    font: inherit;
    font-weight: 600;
    color: var(--text);
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .link:hover,
  .link:focus-visible {
    text-decoration: underline;
  }
  .plain {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 2px var(--space-3);
    cursor: pointer;
  }
  .drawer {
    margin: 0 0 0 auto;
    height: 100vh;
    max-height: 100vh;
    width: min(720px, 100vw);
    border: 0;
    border-inline-start: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    padding: var(--space-4);
    overflow-y: auto;
  }
  :global([dir='rtl']) .drawer {
    margin: 0 auto 0 0;
  }
  .drawer-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .drawer-head h2 {
    margin: 0;
    font-size: 16px;
    overflow-wrap: anywhere;
  }
</style>

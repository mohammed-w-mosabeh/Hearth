<script lang="ts">
  import TermPane from './TermPane.svelte';
  import { execURL } from '../lib/docker';
  import { t } from '../lib/i18n';
  import { href } from '../lib/router.svelte';
  import { openContainer } from '../lib/shell';
  import type { Phase } from '../lib/shell';

  // A shell inside a container (SECURITY S12a): /bin/sh through
  // hearth-broker, which asks for the administrator password every time.
  // Opened only on request, never just by showing the tab.
  let { id, name, refused }: { id: string; name: string; refused: string[] } = $props();

  let phase = $state<'idle' | Phase>('idle');
  let error = $state('');
  let session = $state(0); // bumped to open a new shell

  function open(): void {
    error = '';
    session++;
  }
</script>

{#if refused.length > 0}
  <p class="note warn" role="note">{t('shell.refused', { why: refused.join(', ') })}</p>
{:else}
  <div class="bar">
    {#if phase === 'idle' || phase === 'exit'}
      <button type="button" class="plain" onclick={open}>{phase === 'exit' ? t('shell.reopen') : t('shell.open')}</button>
    {/if}
    <span class="note" role="status">
      {#if phase === 'idle'}{t('shell.intro')}{:else if phase === 'auth'}{t('shell.auth')}{:else if phase === 'exit'}{error
          ? t('shell.failed', { error })
          : t('shell.exited')}{/if}
    </span>
    <a class="tab-link" href={href('terminal', openContainer(id, name))}>{t('shell.inTerminal')}</a>
  </div>
  <TermPane
    url={(cols, rows) => execURL(location.href, id, cols, rows)}
    {session}
    onphase={(p, e) => {
      phase = p;
      error = e;
    }}
  />
{/if}

<style>
  .bar {
    display: flex;
    gap: var(--space-3);
    align-items: center;
    margin-bottom: var(--space-2);
    font-size: 13px;
  }
  .note {
    color: var(--text-muted);
    font-size: 13px;
  }
  .tab-link {
    margin-inline-start: auto;
    font-size: 13px;
    color: var(--accent);
  }
  .warn {
    border-inline-start: 3px solid var(--warn);
    padding-inline-start: var(--space-2);
  }
  .plain {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
</style>

<script lang="ts">
  import AlertBody from './inspect/AlertBody.svelte';
  import ContainerBody from './inspect/ContainerBody.svelte';
  import PackageBody from './inspect/PackageBody.svelte';
  import PortBody from './inspect/PortBody.svelte';
  import ProcBody from './inspect/ProcBody.svelte';
  import UnitBody from './inspect/UnitBody.svelte';
  import { t } from '../lib/i18n';
  import { formatInspect, parseInspect } from '../lib/inspect';
  import { inspectHref, router } from '../lib/router.svelte';
  import type { Live } from '../lib/ws';

  // The inspector drawer (ROADMAP 8.7): one item of a list, on the
  // inline-end side, opened by the hash's inspect parameter. A modal
  // <dialog>: focus stays inside and Esc closes it; focus then goes back to
  // what opened it.
  let { live }: { live: Live } = $props();

  let dialog: HTMLDialogElement;
  let opener: HTMLElement | null = null;

  const item = $derived(parseInspect(router.inspect));
  // Re-keyed per item, so a body starts fresh when a chain link is followed.
  const itemKey = $derived(item ? formatInspect(item) : '');

  $effect(() => {
    // A link that names no item still opens, to say so.
    if (router.inspect && !dialog.open) {
      const a = document.activeElement;
      opener = a instanceof HTMLElement && a !== document.body ? a : null;
      dialog.showModal();
    } else if (!router.inspect && dialog.open) {
      dialog.close();
    }
  });

  // Tab wraps inside the drawer: a modal dialog alone lets it move on to
  // the browser's own controls. A confirmation opened from the drawer is a
  // dialog of its own and keeps its own order.
  const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

  function onkeydown(e: KeyboardEvent): void {
    if (e.key !== 'Tab' || !(e.target instanceof HTMLElement) || e.target.closest('dialog') !== dialog) return;
    const all = [...dialog.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.closest('dialog') === dialog && el.getClientRects().length > 0);
    const first = all[0];
    const last = all[all.length - 1];
    if (!first || !last) return;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  function close(): void {
    if (router.inspect) location.hash = inspectHref('');
  }

  function onclose(): void {
    close();
    const back = opener;
    opener = null;
    // The opener may have gone with a list refresh: then the page's heading.
    if (back?.isConnected) back.focus();
    else document.querySelector<HTMLElement>('main h1')?.focus();
  }
</script>

<dialog bind:this={dialog} class="drawer" aria-labelledby="inspect-title" {onclose} {onkeydown}>
  <button type="button" class="x" aria-label={t('inspect.close')} onclick={() => dialog.close()}>×</button>
  {#if item}
    {#key itemKey}
      {#if item.kind === 'unit'}
        <UnitBody {item} {live} />
      {:else if item.kind === 'container'}
        <ContainerBody {item} {live} />
      {:else if item.kind === 'package'}
        <PackageBody {item} />
      {:else if item.kind === 'port'}
        <PortBody {item} />
      {:else if item.kind === 'proc'}
        <ProcBody {item} {live} />
      {:else if item.kind === 'alert'}
        <AlertBody {item} />
      {/if}
    {/key}
  {:else if router.inspect}
    <h2 id="inspect-title">{t('inspect.unknown')}</h2>
  {/if}
</dialog>

<style>
  .drawer {
    position: fixed;
    inset-block: 0;
    inset-inline: auto 0;
    margin: 0;
    width: min(420px, 100vw);
    max-width: 100vw;
    height: 100dvh;
    max-height: 100dvh;
    padding: var(--space-5);
    overflow-y: auto;
    background: var(--surface);
    color: var(--text);
    border: 0;
    border-inline-start: 1px solid var(--border);
    box-shadow: var(--shadow);
  }
  .drawer::backdrop {
    background: rgb(0 0 0 / 0.35);
  }
  .x {
    position: absolute;
    inset-block-start: var(--space-3);
    inset-inline-end: var(--space-3);
    font: inherit;
    font-size: 20px;
    line-height: 1;
    padding: var(--space-1) var(--space-2);
    color: var(--text-muted);
    background: none;
    border: 0;
    border-radius: var(--radius-s);
    cursor: pointer;
  }
  .x:hover {
    color: var(--text);
    background: var(--surface-2);
  }
  /* Shared by the bodies. */
  .drawer :global(h2) {
    font-size: 18px;
    margin: 0 var(--space-6) var(--space-1) 0;
    margin-inline: 0 var(--space-6);
    overflow-wrap: anywhere;
  }
  .drawer :global(h3) {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: var(--space-5) 0 var(--space-2);
  }
  .drawer :global(.sub) {
    color: var(--text-muted);
    margin: 0 0 var(--space-3);
    overflow-wrap: anywhere;
  }
  .drawer :global(dl) {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--space-1) var(--space-4);
    margin: 0;
  }
  .drawer :global(dt) {
    color: var(--text-muted);
  }
  .drawer :global(dd) {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .drawer :global(.mono) {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
  }
  .drawer :global(.actions) {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  .drawer :global(.actions button),
  .drawer :global(.actions a) {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    text-decoration: none;
  }
  .drawer :global(.actions button:hover:not(:disabled)),
  .drawer :global(.actions a:hover) {
    background: var(--surface-2);
  }
  .drawer :global(.actions button:disabled) {
    opacity: 0.4;
    cursor: default;
  }
  .drawer :global(.muted) {
    color: var(--text-muted);
  }
  .drawer :global(.error) {
    color: var(--crit);
  }
</style>

<script lang="ts">
  import { t } from '../lib/i18n';
  import { routes } from '../lib/router.svelte';
  import { GOTO } from '../lib/shortcuts';

  // Lists the keyboard shortcuts. Native modal <dialog>: focus is trapped
  // and Esc closes it.
  let { open, onclose }: { open: boolean; onclose: () => void } = $props();

  let dialog: HTMLDialogElement;

  $effect(() => {
    if (open && !dialog.open) dialog.showModal();
    else if (!open && dialog.open) dialog.close();
  });

  const pages = Object.entries(GOTO).flatMap(([k, id]) => {
    const r = routes.find((x) => x.id === id);
    return r ? [{ k, label: r.label }] : [];
  });
</script>

<dialog bind:this={dialog} aria-labelledby="keys-title" onclose={() => open && onclose()}>
  <h2 id="keys-title">{t('keys.title')}</h2>
  <dl>
    <div><dt><kbd>Ctrl</kbd> <kbd>K</kbd></dt><dd>{t('keys.palette')}</dd></div>
    <div><dt><kbd>?</kbd></dt><dd>{t('keys.help')}</dd></div>
    {#each pages as p (p.k)}
      <div><dt><kbd>g</kbd> <kbd>{p.k}</kbd></dt><dd>{t(p.label)}</dd></div>
    {/each}
  </dl>
  <p>{t('keys.hint')}</p>
  <div class="actions"><button type="button" onclick={onclose}>{t('keys.close')}</button></div>
</dialog>

<style>
  dialog {
    width: min(460px, calc(100vw - 32px));
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    background: var(--surface);
    color: var(--text);
    padding: var(--space-4);
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 40%);
  }
  h2 {
    font-size: 16px;
    margin: 0 0 var(--space-3);
  }
  dl {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
    gap: var(--space-1) var(--space-4);
    margin: 0;
  }
  dl div {
    display: flex;
    justify-content: space-between;
    gap: var(--space-2);
    align-items: center;
  }
  dd {
    margin: 0;
    font-size: 13px;
  }
  dt {
    flex: none;
  }
  kbd {
    font-family: var(--font-mono);
    font-size: 12px;
    padding: 0 var(--space-1);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    background: var(--surface-2);
  }
  p {
    font-size: 12px;
    color: var(--text-muted);
    margin: var(--space-3) 0 0;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    margin-top: var(--space-3);
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
</style>

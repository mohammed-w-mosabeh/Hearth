<script lang="ts">
  import { t } from '../lib/i18n';
  import { dismiss, toasts } from '../lib/toast.svelte';

  // Each toast is a manual popover: in the top layer, above dialogs'
  // backdrops' siblings, and not light-dismissed by clicks elsewhere.
  function show(el: HTMLElement) {
    el.showPopover();
  }
</script>

<div class="stack" aria-live="polite">
  {#each toasts as item, i (item.id)}
    <div class="toast {item.tone}" popover="manual" role="status" use:show style:--i={toasts.length - 1 - i}>
      <span>{item.text}</span>
      {#if item.undo}
        <button
          type="button"
          onclick={() => {
            item.undo?.();
            dismiss(item.id);
          }}>{t('toast.undo')}</button
        >
      {/if}
      <button type="button" class="close" aria-label={t('toast.close')} onclick={() => dismiss(item.id)}>✕</button>
    </div>
  {/each}
</div>

<style>
  .toast {
    position: fixed;
    inset: auto;
    inset-block-end: var(--space-5);
    inset-inline-end: var(--space-5);
    /* Stack upward: newest at the bottom. */
    translate: 0 calc(var(--i) * -56px);
    margin: 0;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    max-width: min(480px, calc(100vw - 2 * var(--space-5)));
    padding: var(--space-2) var(--space-3);
    background: var(--text);
    color: var(--bg);
    border: 0;
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    font-size: 13px;
    opacity: 1;
    transition:
      opacity var(--motion),
      translate var(--motion),
      display var(--motion) allow-discrete,
      overlay var(--motion) allow-discrete;
  }
  @starting-style {
    .toast:popover-open {
      opacity: 0;
    }
  }
  .toast.error {
    background: var(--crit);
    color: #fff;
  }
  button {
    font: inherit;
    font-weight: 600;
    background: none;
    border: 0;
    color: inherit;
    cursor: pointer;
    padding: var(--space-1);
    text-decoration: underline;
  }
  .close {
    text-decoration: none;
    opacity: 0.7;
  }
</style>

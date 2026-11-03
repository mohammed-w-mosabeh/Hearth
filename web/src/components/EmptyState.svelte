<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  // A page with nothing to show (inline: false), or a list with nothing in it
  // yet (inline): what is missing, why, and how to get some.
  let {
    title = '',
    body,
    icon = '',
    inline = false,
    children,
  }: { title?: string; body: string; icon?: string; inline?: boolean; children?: Snippet } = $props();
</script>

<div class="empty" class:inline>
  {#if icon}<span class="icon" aria-hidden="true"><Icon name={icon} size={20} /></span>{/if}
  {#if title}<h2>{title}</h2>{/if}
  {#if body}<p>{body}</p>{/if}
  {#if children}<div class="actions">{@render children()}</div>{/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-2);
    max-width: 48ch;
    margin: 12vh auto 0;
    padding: var(--space-5);
    text-align: center;
    color: var(--text-muted);
  }
  .empty.inline {
    max-width: none;
    margin: var(--space-3) 0;
    background: var(--surface);
    border: 1px dashed var(--border);
    border-radius: var(--radius-m);
  }
  .inline p {
    max-width: 60ch;
  }
  .icon {
    display: grid;
    place-items: center;
    width: 40px;
    height: 40px;
    border-radius: 50%;
    background: var(--surface-2);
    color: var(--text-muted);
  }
  h2 {
    margin: 0;
    color: var(--text);
    font-size: 18px;
  }
  p {
    margin: 0;
  }
  .actions {
    margin-top: var(--space-2);
  }
</style>

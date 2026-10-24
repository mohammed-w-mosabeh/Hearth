<script lang="ts">
  import type { Snippet } from 'svelte';

  let { title, body, children }: { title: string; body: string; children?: Snippet } = $props();
</script>

<div class="empty">
  <h2>{title}</h2>
  <p>{body}</p>
  {@render children?.()}
</div>

<style>
  .empty {
    max-width: 44ch;
    margin: 12vh auto 0;
    text-align: center;
    color: var(--text-muted);
  }
  h2 {
    color: var(--text);
    font-size: 18px;
    margin-bottom: var(--space-2);
  }
</style>

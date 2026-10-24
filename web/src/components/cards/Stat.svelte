<script lang="ts">
  import type { Snippet } from 'svelte';

  // The headline number of a card with an optional detail line.
  let { value, children }: { value: string; children?: Snippet } = $props();
</script>

<div class="stat">
  <span class="value">{value}</span>
  {#if children}<span class="detail">{@render children()}</span>{/if}
</div>

<style>
  .stat {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: var(--space-2);
  }
  .value {
    font-family: var(--font-mono);
    font-size: 24px;
    font-variant-numeric: tabular-nums;
    line-height: 1.2;
  }
  .detail {
    color: var(--text-muted);
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
</style>

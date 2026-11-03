<script lang="ts">
  // Loading placeholder: what is being read, over the rough shape of what
  // will come. Still, not shimmering: nothing loops (ROADMAP 8.9).
  let { label, rows = 3 }: { label: string; rows?: number } = $props();
</script>

<div class="skeleton" role="status" aria-busy="true">
  <p>{label}</p>
  {#each { length: rows }, i (i)}
    <span class="bar" aria-hidden="true"></span>
  {/each}
</div>

<style>
  .skeleton {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    margin: var(--space-3) 0;
    max-width: 60ch;
  }
  p {
    margin: 0;
    color: var(--text-muted);
    font-size: 13px;
  }
  .bar {
    display: block;
    height: 12px;
    border-radius: var(--radius-s);
    background: var(--surface-2);
  }
  .bar:nth-of-type(3n + 1) {
    width: 92%;
  }
  .bar:nth-of-type(3n + 2) {
    width: 76%;
  }
  .bar:nth-of-type(3n) {
    width: 58%;
  }
</style>

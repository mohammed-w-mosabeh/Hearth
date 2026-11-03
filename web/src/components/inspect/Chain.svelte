<script lang="ts">
  import { t } from '../../lib/i18n';
  import { formatInspect, type Link } from '../../lib/inspect';
  import { inspectHref } from '../../lib/router.svelte';

  // The lineage as a chain: port → process → unit → package → container.
  // Each link opens that item in the drawer.
  let { links }: { links: Link[] } = $props();
</script>

{#if links.length > 1}
  <h3>{t('inspect.lineage')}</h3>
  <ol class="chain">
    {#each links as l, i (i)}
      <li>
        {#if i > 0}<span class="arrow" aria-hidden="true">→</span>{/if}
        {#if l.item}
          <a href={inspectHref(formatInspect(l.item))}>{l.label}</a>
        {:else}
          <span aria-current="true">{l.label}</span>
        {/if}
      </li>
    {/each}
  </ol>
{/if}

<style>
  .chain {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1);
  }
  li {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    min-width: 0;
  }
  a,
  span[aria-current] {
    display: inline-block;
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 2px var(--space-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    font-family: var(--font-mono);
    font-size: 12px;
  }
  a {
    color: var(--text);
    text-decoration: none;
  }
  a:hover {
    background: var(--surface-2);
  }
  span[aria-current] {
    border-color: var(--accent);
  }
  .arrow {
    color: var(--text-muted);
  }
  :global([dir='rtl']) .arrow {
    display: inline-block;
    transform: scaleX(-1);
  }
</style>

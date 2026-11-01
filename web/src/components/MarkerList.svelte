<script lang="ts">
  import { locale, t } from '../lib/i18n';
  import { markerText, type Marker } from '../lib/markers';

  // The events on the charts, for reading without a pointer. Subjects come
  // from the system: plain text, escaped by Svelte.
  let { markers }: { markers: Marker[] } = $props();
</script>

{#if markers.length > 0}
  <details class="markers">
    <summary>{t('marker.list', { n: markers.length })}</summary>
    <p>{t('marker.hint')}</p>
    <ul>
      {#each markers as m, i (`${m.t}-${i}`)}
        <li><time datetime={new Date(m.t).toISOString()}>{new Date(m.t).toLocaleString(locale('en-GB'))}</time> {markerText(m)}</li>
      {/each}
    </ul>
  </details>
{/if}

<style>
  .markers {
    margin-bottom: var(--space-3);
    font-size: 13px;
  }
  summary {
    cursor: pointer;
    color: var(--text-muted);
  }
  p {
    margin: var(--space-1) 0;
    font-size: 12px;
    color: var(--text-muted);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 200px;
    overflow-y: auto;
  }
  li {
    overflow-wrap: anywhere;
  }
  time {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    margin-inline-end: var(--space-2);
  }
</style>

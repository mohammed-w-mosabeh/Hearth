<script lang="ts">
  import Stat from './Stat.svelte';
  import { bytes, pct } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import type { UsageMsg } from '../../lib/docker';

  // Containers at a glance (ROADMAP 2.11), read by hearthd from cgroups.
  let { usage }: { usage: UsageMsg } = $props();
  const top = $derived(usage.running.slice(0, 5));
  const mem = $derived(usage.running.reduce((a, u) => a + u.mem, 0));
</script>

<Stat value={t('card.docker.running', { n: usage.running.length })}>
  {t('card.docker.detail', { stopped: usage.stopped, mem: bytes(mem) })}
</Stat>
{#if usage.unexpected.length > 0}
  <p class="bad" role="status">{t('card.docker.unexpected', { names: usage.unexpected.join(', ') })}</p>
{/if}
{#if top.length > 0}
  <table>
    <thead>
      <tr><th scope="col">{t('card.docker.name')}</th><th scope="col">{t('card.docker.cpu')}</th><th scope="col">{t('card.docker.mem')}</th></tr>
    </thead>
    <tbody>
      {#each top as u (u.id)}
        <tr>
          <td><a href={`#/docker?q=${encodeURIComponent(u.name)}`} title={u.name}>{u.name}</a></td>
          <td>{pct(u.cpu, 1)}</td>
          <td>{bytes(u.mem)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{:else}
  <p class="muted">{t('card.docker.none')}</p>
{/if}

<style>
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
    table-layout: fixed;
  }
  th {
    color: var(--text-muted);
    font-weight: 500;
    text-align: end;
  }
  th:first-child,
  td:first-child {
    text-align: start;
    width: 55%;
  }
  td {
    text-align: end;
    font-variant-numeric: tabular-nums;
    padding: 1px 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  a {
    color: var(--text);
  }
  .bad {
    margin: 0 0 var(--space-2);
    font-size: 12px;
    color: var(--crit);
  }
  .muted {
    margin: 0;
    font-size: 12px;
    color: var(--text-muted);
  }
</style>

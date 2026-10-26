<script lang="ts">
  import Icon from './Icon.svelte';
  import { t } from '../lib/i18n';
  import { href, router, routes } from '../lib/router.svelte';
  import type { WsState } from '../lib/ws';

  let { status, onsearch }: { status: WsState; onsearch: () => void } = $props();
  const statusKey = { connected: 'status.connected', connecting: 'status.connecting', offline: 'status.offline' } as const;
</script>

<nav aria-label={t('nav.label')}>
  <a class="brand" href={href('home')} title={t('app.name')}>
    <Icon name="flame" size={24} />
    <span class="visually-hidden">{t('app.name')}</span>
  </a>
  <button type="button" class="search" title={t('palette.open')} onclick={onsearch}>
    <Icon name="search" />
    <span class="visually-hidden">{t('palette.open')}</span>
  </button>
  <ul>
    {#each routes as r (r.id)}
      <li>
        <a href={href(r.id)} title={t(r.label)} aria-current={router.id === r.id ? 'page' : undefined}>
          <Icon name={r.icon} />
          <span class="visually-hidden">{t(r.label)}</span>
        </a>
      </li>
    {/each}
  </ul>
  <div class="status" data-state={status} title={t(statusKey[status])} role="status">
    <span class="dot"></span>
    <span class="visually-hidden">{t(statusKey[status])}</span>
  </div>
</nav>

<style>
  nav {
    width: var(--sidebar-w);
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-3) 0;
    background: var(--surface);
    border-inline-end: 1px solid var(--border);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    flex: 1;
  }
  a,
  .search {
    display: grid;
    place-items: center;
    width: 40px;
    height: 40px;
    border-radius: var(--radius-m);
    color: var(--text-muted);
    transition:
      background var(--motion),
      color var(--motion);
  }
  .search {
    font: inherit;
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  a:hover,
  .search:hover {
    background: var(--surface-2);
    color: var(--text);
  }
  a[aria-current='page'] {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .brand {
    color: var(--accent);
    margin-bottom: var(--space-2);
  }
  .status {
    display: grid;
    place-items: center;
    width: 40px;
    height: 24px;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
  }
  [data-state='connected'] .dot {
    background: var(--ok);
  }
  [data-state='offline'] .dot {
    background: var(--crit);
  }
</style>

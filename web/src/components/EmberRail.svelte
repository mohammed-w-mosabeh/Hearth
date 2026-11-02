<script lang="ts">
  import Icon from './Icon.svelte';
  import { loadRailCollapsed, railGroups, saveRailCollapsed } from '../lib/ember';
  import { t } from '../lib/i18n';
  import { href, router } from '../lib/router.svelte';
  import type { WsState } from '../lib/ws';

  let { status, onsearch }: { status: WsState; onsearch: () => void } = $props();
  const statusKey = { connected: 'status.connected', connecting: 'status.connecting', offline: 'status.offline' } as const;

  let collapsed = $state(loadRailCollapsed());
  function toggle(): void {
    collapsed = !collapsed;
    saveRailCollapsed(collapsed);
  }
  const toggleLabel = $derived(t(collapsed ? 'rail.expand' : 'rail.collapse'));
</script>

<nav aria-label={t('nav.label')} class:collapsed>
  <div class="top">
    <a class="brand" href={href('home')} title={t('app.name')}>
      <Icon name="flame" size={22} />
      <span class="label">{t('app.name')}</span>
    </a>
    <button type="button" class="tool toggle" title={toggleLabel} aria-expanded={!collapsed} onclick={toggle}>
      <span class="chevron" aria-hidden="true"></span>
      <span class="visually-hidden">{toggleLabel}</span>
    </button>
  </div>
  <button type="button" class="tool search" title={t('palette.open')} onclick={onsearch}>
    <Icon name="search" size={18} />
    <span class="label">{t('rail.search')}</span>
    <kbd class="label">Ctrl K</kbd>
  </button>
  <div class="groups">
    {#each railGroups as g (g.label)}
      <section aria-label={t(g.label)}>
        <h2 class="label">{t(g.label)}</h2>
        <ul>
          {#each g.items as item (item.id)}
            <li>
              <a href={href(item.id)} title={t(item.label)} aria-current={router.id === item.id ? 'page' : undefined}>
                <Icon name={item.icon} size={18} />
                <span class="label">{t(item.label)}</span>
              </a>
            </li>
          {/each}
        </ul>
      </section>
    {/each}
  </div>
  <div class="status" data-state={status} role="status">
    <span class="dot" aria-hidden="true"></span>
    <span class="label">{t(statusKey[status])}</span>
    <span class="visually-hidden">{t(statusKey[status])}</span>
  </div>
</nav>

<style>
  nav {
    flex: none;
    width: var(--rail-w);
    height: 100%;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-2);
    background: var(--surface);
    border-inline-end: 1px solid var(--border);
    transition: width var(--motion);
  }
  nav.collapsed {
    width: var(--rail-w-collapsed);
  }
  nav.collapsed .label {
    display: none;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-1);
  }
  nav.collapsed .top {
    flex-direction: column;
  }
  .groups {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    scrollbar-width: thin;
  }
  section + section {
    margin-block-start: var(--space-2);
  }
  nav.collapsed section + section {
    border-block-start: 1px solid var(--border);
    padding-block-start: var(--space-2);
    margin-block-start: var(--space-2);
  }
  h2 {
    padding: 0 var(--space-2);
    margin-block-end: var(--space-1);
    font-size: 11px;
    letter-spacing: 0.04em;
    color: var(--text-muted);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  a,
  .tool {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 30px;
    padding: 0 var(--space-2);
    border: 0;
    border-radius: var(--radius-s);
    background: none;
    color: var(--text-muted);
    font: inherit;
    text-decoration: none;
    cursor: pointer;
    transition:
      background var(--motion),
      color var(--motion);
  }
  li a {
    border-inline-start: 2px solid transparent;
  }
  a:hover,
  .tool:hover {
    background: var(--surface-2);
    color: var(--text);
  }
  li a[aria-current='page'] {
    background: var(--surface-2);
    color: var(--text);
    border-inline-start-color: var(--accent);
    font-weight: 600;
  }
  .brand {
    color: var(--text);
    font-weight: 600;
    flex: 1;
    min-width: 0;
  }
  .search {
    width: 100%;
    border: 1px solid var(--border);
  }
  .search .label:first-of-type {
    flex: 1;
    text-align: start;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  kbd {
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .chevron {
    width: 8px;
    height: 8px;
    border-block-end: 2px solid currentcolor;
    border-inline-start: 2px solid currentcolor;
    transform: rotate(45deg);
    transition: transform var(--motion);
  }
  :global([dir='rtl']) .chevron {
    transform: rotate(-45deg);
  }
  nav.collapsed .chevron {
    transform: rotate(225deg);
  }
  :global([dir='rtl']) nav.collapsed .chevron {
    transform: rotate(135deg);
  }
  .status {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-1) var(--space-2);
    font-size: 12px;
    color: var(--text-muted);
  }
  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
  }
  [data-state='connected'] .dot {
    background: var(--accent);
  }
  [data-state='offline'] .dot {
    border-radius: 0;
    background: var(--crit);
  }
  @media (max-width: 900px) {
    nav {
      width: var(--rail-w-collapsed);
    }
    nav .label {
      display: none;
    }
  }
</style>

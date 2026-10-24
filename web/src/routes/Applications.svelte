<script lang="ts">
  import { api } from '../lib/api';
  import { CATEGORIES, SOURCES, filterApps, iconQuery, initials, rank, type App, type SourceFilter } from '../lib/apps';
  import { t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  let { live }: { live: Live } = $props();

  const ICON_BATCH = 60; // server limit per request

  let apps = $state.raw<App[] | null>(null);
  let failed = $state(false);
  let icons = $state<Record<string, string>>({});
  let favorites = $state<string[]>([]);
  let search = $state('');
  let source = $state<SourceFilter>('all');
  let category = $state('');

  async function load(): Promise<void> {
    try {
      const res = await api<{ apps: App[] }>('/apps');
      apps = res.apps;
      failed = false;
      await loadIcons(res.apps);
    } catch {
      failed = true;
    }
  }

  // Icons arrive as data: URIs in batches (an <img> cannot send the key).
  async function loadIcons(list: App[]): Promise<void> {
    const want = list.filter((a) => a.has_icon && !(a.id in icons)).map((a) => a.id);
    for (let i = 0; i < want.length; i += ICON_BATCH) {
      try {
        const got = await api<Record<string, string>>(`/apps/icons?${iconQuery(want.slice(i, i + ICON_BATCH))}`);
        for (const [id, uri] of Object.entries(got)) {
          if (uri.startsWith('data:image/png;base64,') || uri.startsWith('data:image/svg+xml;base64,')) icons[id] = uri;
        }
      } catch {
        return; // letters stay in place of icons
      }
    }
  }

  $effect(() => {
    void load();
    api<{ ids?: unknown } | null>('/settings/apps.favorites')
      .then((v) => {
        if (v && Array.isArray(v.ids)) favorites = v.ids.filter((x): x is string => typeof x === 'string');
      })
      .catch(() => {});
    // The server says when .desktop files or AppImages change.
    return live.subscribe('apps', () => void load());
  });

  async function toggleFavorite(a: App): Promise<void> {
    const before = favorites;
    favorites = favorites.includes(a.id) ? favorites.filter((x) => x !== a.id) : [...favorites, a.id];
    try {
      await api('/settings/apps.favorites', { method: 'PUT', body: JSON.stringify({ ids: favorites }) });
    } catch {
      favorites = before;
      toast(t('apps2.favFailed'), { tone: 'error' });
    }
  }

  const shown = $derived(apps ? rank(filterApps(apps, source, category, search), search) : []);
  const favSet = $derived(new Set(favorites));
  // Favorites lead the grid unless the user is searching or filtering.
  const browsing = $derived(search.trim() === '' && source === 'all' && category === '');
  const favs = $derived(browsing ? shown.filter((a) => favSet.has(a.id)) : []);
  const rest = $derived(browsing ? shown.filter((a) => !favSet.has(a.id)) : shown);
  const presentSources = $derived(new Set((apps ?? []).map((a) => a.source)));
</script>

<header>
  <div>
    <h1>{t('apps2.title')}</h1>
    <p>{apps ? t('apps2.count', { n: apps.length }) : t('apps2.intro')}</p>
  </div>
</header>

<div class="bar">
  <input type="search" placeholder={t('apps2.search')} aria-label={t('apps2.search')} bind:value={search} />
  <div class="segmented" role="radiogroup" aria-label={t('apps2.source')}>
    {#each SOURCES.filter((s) => s.id === 'all' || presentSources.has(s.id)) as s (s.id)}
      <button type="button" role="radio" aria-checked={source === s.id} class:on={source === s.id} onclick={() => (source = s.id)}>
        {t(s.label)}
      </button>
    {/each}
  </div>
  <select aria-label={t('apps2.category')} bind:value={category}>
    <option value="">{t('apps2.allCategories')}</option>
    {#each CATEGORIES as c (c.id)}
      <option value={c.id}>{t(c.label)}</option>
    {/each}
  </select>
</div>

{#snippet tile(a: App)}
  <li class="tile">
    <div class="icon" aria-hidden="true">
      {#if icons[a.id]}
        <img src={icons[a.id]} alt="" width="48" height="48" />
      {:else}
        <span class="letters">{initials(a.name)}</span>
      {/if}
    </div>
    <span class="name" title={a.comment ?? a.name}>{a.name}</span>
    {#if a.generic_name}<span class="generic">{a.generic_name}</span>{/if}
    <span class="src">{t(SOURCES.find((s) => s.id === a.source)?.label ?? 'apps2.source.system')}</span>
    <button
      type="button"
      class="star"
      class:on={favSet.has(a.id)}
      aria-pressed={favSet.has(a.id)}
      aria-label={t(favSet.has(a.id) ? 'apps2.unfavorite' : 'apps2.favorite', { name: a.name })}
      onclick={() => toggleFavorite(a)}>{favSet.has(a.id) ? '★' : '☆'}</button
    >
  </li>
{/snippet}

{#if failed}
  <p class="note error" role="status">{t('apps2.error')}</p>
{:else if apps === null}
  <p class="note">{t('apps2.loading')}</p>
{:else if shown.length === 0}
  <p class="note">{t('apps2.none')}</p>
{:else}
  {#if favs.length > 0}
    <h2>{t('apps2.favorites')}</h2>
    <ul class="grid">
      {#each favs as a (a.id)}{@render tile(a)}{/each}
    </ul>
    <h2>{t('apps2.all')}</h2>
  {/if}
  <ul class="grid">
    {#each rest as a (a.id)}{@render tile(a)}{/each}
  </ul>
{/if}

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: var(--space-4) 0 var(--space-2);
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-4);
  }
  input[type='search'],
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
  }
  input[type='search'] {
    min-width: 240px;
  }
  .segmented {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow: hidden;
  }
  .segmented button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 0;
    border-inline-start: 1px solid var(--border);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .segmented button:first-child {
    border-inline-start: 0;
  }
  .segmented button.on {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .note {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .grid {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(132px, 1fr));
    gap: var(--space-3);
  }
  .tile {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    padding: var(--space-4) var(--space-2) var(--space-3);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    text-align: center;
    min-width: 0;
  }
  .icon {
    width: 48px;
    height: 48px;
    margin-bottom: var(--space-2);
    display: grid;
    place-items: center;
  }
  .icon img {
    width: 48px;
    height: 48px;
    object-fit: contain;
  }
  .letters {
    width: 44px;
    height: 44px;
    border-radius: 12px;
    display: grid;
    place-items: center;
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: 600;
  }
  .name {
    font-size: 13px;
    font-weight: 500;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .generic,
  .src {
    font-size: 11px;
    color: var(--text-muted);
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .star {
    position: absolute;
    top: var(--space-1);
    inset-inline-end: var(--space-1);
    font-size: 16px;
    line-height: 1;
    background: none;
    border: 0;
    color: var(--text-muted);
    cursor: pointer;
    padding: var(--space-1);
    opacity: 0;
    transition: opacity var(--motion);
  }
  .tile:hover .star,
  .star:focus-visible,
  .star.on {
    opacity: 1;
  }
  .star.on {
    color: var(--accent);
  }
</style>

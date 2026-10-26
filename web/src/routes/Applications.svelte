<script lang="ts">
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import { preview, run, type Plan } from '../lib/actions';
  import { api, ApiError } from '../lib/api';
  import { CATEGORIES, SOURCES, filterApps, iconQuery, initials, rank, type App, type SourceFilter } from '../lib/apps';
  import { t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  let { live }: { live: Live } = $props();

  const ICON_BATCH = 60; // server limit per request
  const RUNNING_MS = 5000; // how often running apps are re-read while visible

  let apps = $state.raw<App[] | null>(null);
  let failed = $state(false);
  let icons = $state<Record<string, string>>({});
  let favorites = $state<string[]>([]);
  let search = $state('');
  let source = $state<SourceFilter>('all');
  let category = $state('');
  let running = $state.raw<Set<string>>(new Set());
  let opening = $state.raw<ReadonlySet<string>>(new Set());
  let closing = $state<{ app: App; plan: Plan } | null>(null);

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

  async function loadRunning(): Promise<void> {
    try {
      const res = await api<{ running: string[] }>('/apps/running');
      running = new Set(res.running);
    } catch {
      // keep the last known state
    }
  }

  function errorText(err: unknown): string {
    return err instanceof ApiError ? err.message : t('action.failed');
  }

  // Launch (ROADMAP 1.11): the server starts the app in its own systemd
  // unit; it shows up as running once its processes are there.
  async function launch(a: App): Promise<void> {
    if (opening.has(a.id)) return;
    opening = new Set([...opening, a.id]);
    try {
      await run('app', 'launch', a.id);
      toast(t('apps2.opening', { name: a.name }));
      setTimeout(() => void loadRunning(), 1500);
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      opening = new Set([...opening].filter((id) => id !== a.id));
    }
  }

  // Close ends every process of the app: preview, then confirm.
  async function askClose(a: App): Promise<void> {
    try {
      const plan = await preview('app', 'close', a.id);
      if (plan.items.length === 0) {
        toast(t('apps2.cannotClose', { name: a.name }), { tone: 'error' });
        return;
      }
      closing = { app: a, plan };
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
      void loadRunning();
    }
  }

  async function confirmClose(): Promise<void> {
    if (!closing) return;
    const { app: a, plan } = closing;
    try {
      const res = await run('app', 'close', a.id, {}, plan.token);
      toast(t('result.done', { verb: t('action.close'), name: a.name, n: res.done }), { tone: res.done === 0 ? 'error' : 'info' });
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      closing = null;
      setTimeout(() => void loadRunning(), 1000);
    }
  }

  $effect(() => {
    void loadRunning();
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') void loadRunning();
    }, RUNNING_MS);
    return () => clearInterval(timer);
  });

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
  <li class="tile" class:running={running.has(a.id)}>
    <button
      type="button"
      class="open"
      title={a.comment ?? a.name}
      aria-label={t('apps2.open', { name: a.name })}
      aria-busy={opening.has(a.id)}
      onclick={() => launch(a)}
    >
      <span class="icon" aria-hidden="true">
        {#if icons[a.id]}
          <img src={icons[a.id]} alt="" width="48" height="48" />
        {:else}
          <span class="letters">{initials(a.name)}</span>
        {/if}
      </span>
      <span class="name">{a.name}</span>
      {#if a.generic_name}<span class="generic">{a.generic_name}</span>{/if}
      <span class="src">{t(SOURCES.find((s) => s.id === a.source)?.label ?? 'apps2.source.system')}</span>
    </button>
    {#if running.has(a.id)}
      <span class="state"><span class="dot" aria-hidden="true"></span>{t('apps2.running')}</span>
      <button type="button" class="close" aria-label={t('apps2.closeLabel', { name: a.name })} onclick={() => askClose(a)}>
        {t('apps2.close')}
      </button>
    {/if}
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

<ConfirmDialog
  plan={closing?.plan ?? null}
  title={closing ? t('apps2.closeTitle', { name: closing.app.name }) : ''}
  verb={t('apps2.close')}
  onconfirm={confirmClose}
  oncancel={() => (closing = null)}
/>

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
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    text-align: center;
    min-width: 0;
    transition: border-color var(--motion);
  }
  .tile:hover {
    border-color: var(--accent);
  }
  .open {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    width: 100%;
    padding: var(--space-4) var(--space-2) var(--space-3);
    font: inherit;
    color: inherit;
    background: none;
    border: 0;
    border-radius: var(--radius-m);
    cursor: pointer;
    min-width: 0;
  }
  .open[aria-busy='true'] {
    cursor: progress;
    opacity: 0.6;
  }
  .state {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    font-size: 11px;
    color: var(--ok);
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--ok);
  }
  .close {
    font: inherit;
    font-size: 12px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 0 var(--space-3);
    margin: var(--space-1) 0 var(--space-3);
    cursor: pointer;
  }
  .close:hover {
    border-color: var(--crit);
    color: var(--crit);
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

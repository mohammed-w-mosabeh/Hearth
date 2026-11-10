<script lang="ts">
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { failure } from '../lib/errors';
  import { run } from '../lib/actions';
  import { api } from '../lib/api';
  import { uninstallHref } from '../lib/uninstall';
  import { bytes, duration } from '../lib/format';
  import {
    GAME_SORTS,
    GAME_SOURCES,
    coverQuery,
    filterGames,
    isCoverURI,
    lastPlayed,
    played,
    sortGames,
    type Game,
    type GameFilter,
    type GameSort,
  } from '../lib/games';
  import { initials } from '../lib/apps';
  import { t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  const COVER_BATCH = 24; // server limit per request
  const LAUNCHERS: Record<string, string> = { steam: 'Steam', lutris: 'Lutris', heroic: 'Heroic Games Launcher' };

  let games = $state.raw<Game[] | null>(null);
  let failed = $state('');
  let covers = $state<Record<string, string>>({});
  let search = $state('');
  let source = $state<GameFilter>('all');
  let sort = $state<GameSort>('recent');
  let starting = $state.raw<ReadonlySet<string>>(new Set());
  const now = new Date();

  async function load(): Promise<void> {
    try {
      const res = await api<{ games: Game[] }>('/games');
      games = res.games;
      failed = '';
      await loadCovers(res.games);
    } catch (err) {
      failed = failure(err, 'games.error');
    }
  }

  // Covers arrive as data: URIs in batches, like app icons.
  async function loadCovers(list: Game[]): Promise<void> {
    const want = list.filter((g) => g.has_cover && !(g.id in covers)).map((g) => g.id);
    for (let i = 0; i < want.length; i += COVER_BATCH) {
      try {
        const got = await api<Record<string, string>>(`/games/covers?${coverQuery(want.slice(i, i + COVER_BATCH))}`);
        for (const [id, uri] of Object.entries(got)) {
          if (isCoverURI(uri)) covers[id] = uri;
        }
      } catch {
        return; // letters stay in place of covers
      }
    }
  }

  async function play(g: Game): Promise<void> {
    if (starting.has(g.id)) return;
    starting = new Set([...starting, g.id]);
    try {
      await run('game', 'launch', g.id);
      toast(t('games.starting', { name: g.name }));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      starting = new Set([...starting].filter((id) => id !== g.id));
    }
  }

  function lastText(g: Game): string {
    const m = lastPlayed(g.last_played, now);
    return t(m.key, { n: m.n ?? 0, date: m.date ?? '' });
  }

  $effect(() => {
    void load();
  });

  const shown = $derived(games ? sortGames(filterGames(games, source, search), sort) : []);
  const present = $derived(new Set((games ?? []).map((g) => g.source)));
  const sourceLabel = (s: string) => t(GAME_SOURCES.find((x) => x.id === s)?.label ?? 'games.source.desktop');
</script>

<header>
  <h1>{t('games.title')}</h1>
  <p>{games ? t('games.count', { n: games.length }) : t('games.intro')}</p>
</header>

<div class="bar">
  <input type="search" placeholder={t('games.search')} aria-label={t('games.search')} bind:value={search} />
  <div class="segmented" role="radiogroup" aria-label={t('games.source')}>
    {#each GAME_SOURCES.filter((s) => s.id === 'all' || present.has(s.id)) as s (s.id)}
      <button type="button" role="radio" aria-checked={source === s.id} class:on={source === s.id} onclick={() => (source = s.id)}>
        {t(s.label)}
      </button>
    {/each}
  </div>
  <select aria-label={t('games.sortLabel')} bind:value={sort}>
    {#each GAME_SORTS as s (s.id)}
      <option value={s.id}>{t(s.label)}</option>
    {/each}
  </select>
</div>

{#if failed}
  <p class="note error" role="status">{failed}</p>
{:else if games === null}
  <Skeleton label={t('games.loading')} />
{:else if games.length === 0}
  <EmptyState inline icon="gamepad" body={t('games.empty')} />
{:else if shown.length === 0}
  <p class="note">{t('games.none')}</p>
{:else}
  <ul class="grid">
    {#each shown as g (g.id)}
      <li class="card">
        <div class="cover" class:icon={g.source === 'desktop'} aria-hidden="true">
          {#if covers[g.id]}
            <img src={covers[g.id]} alt="" loading="lazy" />
          {:else}
            <span class="letters">{initials(g.name)}</span>
          {/if}
        </div>
        <div class="body">
          <span class="name" title={g.name}>{g.name}</span>
          <span class="meta">{sourceLabel(g.source)}{#if g.store}&nbsp;· {g.store}{/if}{#if g.size}&nbsp;· {bytes(g.size)}{/if}</span>
          <span class="meta">{lastText(g)}</span>
          {#if played(g) > 0}
            <span class="meta" title={g.source === 'desktop' ? t('games.trackedNote') : undefined}>
              {t('games.playtime', { time: duration(played(g)) })}
            </span>
          {/if}
          {#if g.launchable}
            <button type="button" class="play" aria-label={t('games.playLabel', { name: g.name })} aria-busy={starting.has(g.id)} onclick={() => play(g)}>
              {t('games.play')}
            </button>
          {:else}
            <span class="meta hint">{t('games.noLauncher', { launcher: LAUNCHERS[g.source] ?? g.source })}</span>
          {/if}
          <a class="meta uninstall" href={uninstallHref(`game:${g.id}`)} aria-label={t('uninstall.menuFor', { name: g.name })}>{t('uninstall.menu')}</a>
        </div>
      </li>
    {/each}
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
    grid-template-columns: repeat(auto-fill, minmax(168px, 1fr));
    gap: var(--space-3);
  }
  .card {
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    overflow: hidden;
    min-width: 0;
    transition: border-color var(--motion);
  }
  .card:hover {
    border-color: var(--accent);
  }
  .cover {
    aspect-ratio: 2 / 3;
    display: grid;
    place-items: center;
    background: var(--accent-soft);
  }
  .cover img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .cover.icon img {
    width: 50%;
    height: auto;
    object-fit: contain;
  }
  .letters {
    font-size: 28px;
    font-weight: 600;
    color: var(--accent);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--space-2) var(--space-3) var(--space-3);
    min-width: 0;
  }
  .name {
    font-size: 13px;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta {
    font-size: 11px;
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .hint {
    white-space: normal;
    margin-top: var(--space-2);
  }
  .play {
    align-self: flex-start;
    margin-top: var(--space-2);
    font: inherit;
    font-size: 12px;
    color: var(--accent-contrast);
    background: var(--accent);
    border: 0;
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-4);
    cursor: pointer;
  }
  .uninstall {
    margin-top: var(--space-1);
    color: var(--text-muted);
  }
  .play[aria-busy='true'] {
    cursor: progress;
    opacity: 0.6;
  }
</style>

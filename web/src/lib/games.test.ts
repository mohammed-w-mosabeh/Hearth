import { describe, expect, it } from 'vitest';
import { filterGames, isCoverURI, lastPlayed, played, sortGames, type Game } from './games';

const game = (id: string, name: string, source: Game['source'], extra: Partial<Game> = {}): Game => ({
  id, name, source, has_cover: false, launchable: true, ...extra,
});

const games = [
  game('steam.220', 'Half-Life 2', 'steam', { last_played: 1800000000, playtime: 5400 }),
  game('heroic.gog.1', 'The Witcher', 'heroic', { store: 'gog', last_played: 1700000000, playtime: 9000 }),
  game('desktop.supertux2', 'SuperTux', 'desktop', { tracked: 600 }),
  game('lutris.3', 'osu!', 'lutris'),
];

describe('games', () => {
  it('filters by source and words, including the store', () => {
    expect(filterGames(games, 'steam', '').map((g) => g.id)).toEqual(['steam.220']);
    expect(filterGames(games, 'all', 'gog').map((g) => g.id)).toEqual(['heroic.gog.1']);
    expect(filterGames(games, 'all', 'half life').map((g) => g.id)).toEqual(['steam.220']);
    expect(filterGames(games, 'all', 'half-life').map((g) => g.id)).toEqual(['steam.220']);
  });
  it('sorts by recency, playtime and name', () => {
    expect(sortGames(games, 'recent').map((g) => g.id)).toEqual(['steam.220', 'heroic.gog.1', 'lutris.3', 'desktop.supertux2']);
    expect(sortGames(games, 'playtime').map((g) => g.id)).toEqual(['heroic.gog.1', 'steam.220', 'desktop.supertux2', 'lutris.3']);
    expect(sortGames(games, 'name').map((g) => g.name)).toEqual(['Half-Life 2', 'osu!', 'SuperTux', 'The Witcher']);
  });
  it('takes the larger of the two playtimes', () => {
    expect(played(game('a', 'a', 'desktop', { tracked: 60, playtime: 0 }))).toBe(60);
    expect(played(game('a', 'a', 'desktop'))).toBe(0);
  });
  it('says when a game was last played', () => {
    const now = new Date(2026, 9, 29, 20, 0);
    const at = (d: Date) => Math.floor(d.getTime() / 1000);
    expect(lastPlayed(undefined, now).key).toBe('games.never');
    expect(lastPlayed(at(new Date(2026, 9, 29, 1, 0)), now).key).toBe('games.today');
    expect(lastPlayed(at(new Date(2026, 9, 28, 23, 0)), now).key).toBe('games.yesterday');
    expect(lastPlayed(at(new Date(2026, 9, 20)), now)).toEqual({ key: 'games.daysAgo', n: 9 });
    expect(lastPlayed(at(new Date(2025, 0, 5)), now)).toEqual({ key: 'games.on', date: 'Jan 5, 2025' });
  });
  it('accepts only image data: URIs as covers', () => {
    expect(isCoverURI('data:image/jpeg;base64,AAAA')).toBe(true);
    expect(isCoverURI('data:text/html;base64,AAAA')).toBe(false);
    expect(isCoverURI('javascript:alert(1)')).toBe(false);
  });
});

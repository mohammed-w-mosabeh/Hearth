// Games page: types, filtering and sorting (internal/discovery games).

import { locale, type MessageKey } from './i18n';

export type GameSource = 'steam' | 'lutris' | 'heroic' | 'desktop';

export interface Game {
  id: string;
  name: string;
  source: GameSource;
  store?: string;
  size?: number;
  last_played?: number; // unix seconds, from the launcher
  playtime?: number; // seconds, from the launcher
  tracked?: number; // seconds Hearth saw it running
  has_cover: boolean;
  launchable: boolean;
}

export type GameFilter = 'all' | GameSource;
export type GameSort = 'name' | 'recent' | 'playtime';

export const GAME_SOURCES: { id: GameFilter; label: MessageKey }[] = [
  { id: 'all', label: 'games.source.all' },
  { id: 'steam', label: 'games.source.steam' },
  { id: 'lutris', label: 'games.source.lutris' },
  { id: 'heroic', label: 'games.source.heroic' },
  { id: 'desktop', label: 'games.source.desktop' },
];

export const GAME_SORTS: { id: GameSort; label: MessageKey }[] = [
  { id: 'recent', label: 'games.sort.recent' },
  { id: 'name', label: 'games.sort.name' },
  { id: 'playtime', label: 'games.sort.playtime' },
];

/** Total time played: the launcher's record, or Hearth's for desktop games. */
export function played(g: Game): number {
  return Math.max(g.playtime ?? 0, g.tracked ?? 0);
}

export function filterGames(games: Game[], source: GameFilter, search: string): Game[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return games.filter((g) => {
    if (source !== 'all' && g.source !== source) return false;
    const hay = `${g.name} ${g.source} ${g.store ?? ''}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

/** Sorted copy; ties and never-played games fall back to name order. */
export function sortGames(games: Game[], by: GameSort): Game[] {
  const byName = (a: Game, b: Game) => a.name.localeCompare(b.name, 'en', { sensitivity: 'base' }) || a.id.localeCompare(b.id);
  const key = (g: Game) => (by === 'recent' ? (g.last_played ?? 0) : by === 'playtime' ? played(g) : 0);
  return [...games].sort((a, b) => key(b) - key(a) || byName(a, b));
}

/** "Last played" as a message: today, yesterday, n days ago, or a date. */
export function lastPlayed(ts: number | undefined, now: Date): { key: MessageKey; n?: number; date?: string } {
  if (!ts) return { key: 'games.never' };
  const then = new Date(ts * 1000);
  const day = (d: Date) => Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()) / 86_400_000;
  const days = day(now) - day(then);
  if (days <= 0) return { key: 'games.today' };
  if (days === 1) return { key: 'games.yesterday' };
  if (days < 30) return { key: 'games.daysAgo', n: days };
  return { key: 'games.on', date: then.toLocaleDateString(locale('en'), { year: 'numeric', month: 'short', day: 'numeric' }) };
}

/** Query string for GET /games/covers. */
export function coverQuery(ids: string[]): string {
  const q = new URLSearchParams();
  for (const id of ids) q.append('id', id);
  return q.toString();
}

const COVER_PREFIXES = ['data:image/jpeg;base64,', 'data:image/png;base64,', 'data:image/webp;base64,', 'data:image/svg+xml;base64,'];

/** Only image data: URIs are used as covers. */
export function isCoverURI(s: string): boolean {
  return COVER_PREFIXES.some((p) => s.startsWith(p));
}

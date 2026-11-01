// Homepage tiles (internal/homepage, topic "homepage").

export interface Tile {
  id: number;
  name: string;
  url: string;
  description: string;
  icon: string; // "builtin:<name>", "upload:<sha256>" or ""
  group: string;
  container: string;
  health_check: boolean;
  check_port: number; // 1–65535: TCP connect to the URL's host; 0: web check of the URL
  position: number;
  icon_data?: string; // data: URI of an uploaded icon
}

export interface Status {
  state: 'up' | 'down' | 'warn';
  code?: number;
  latency_ms: number;
  error?: string;
  cert_expires?: number; // Unix ms, for https
  time: number;
}

export type HomepageMsg = { kind: 'status'; status: Record<string, Status> };

/** The tile form's fields. */
export interface TileForm {
  name: string;
  url: string;
  description: string;
  icon: string;
  group: string;
  container: string;
  health_check: boolean;
  check_port: number;
}

export function emptyForm(): TileForm {
  return { name: '', url: '', description: '', icon: '', group: '', container: '', health_check: true, check_port: 0 };
}

export function formOf(t: Tile): TileForm {
  const { name, url, description, icon, group, container, health_check, check_port } = t;
  return { name, url, description, icon, group, container, health_check, check_port };
}

/** A service hearthd found (GET /homepage/suggestions); never saved by itself. */
export interface Suggestion {
  name: string;
  url: string;
  source: 'container' | 'listener';
  container?: string;
  process?: string;
  title?: string; // the page's <title>
}

/** The tile form pre-filled from a suggestion. */
export function suggestionForm(s: Suggestion): TileForm {
  const description = s.title && s.title !== s.name ? s.title : '';
  return { ...emptyForm(), name: s.name, url: s.url, description, container: s.container ?? '' };
}

/** Built-in icons (components/Icon.svelte names) offered in the form. */
export const BUILTIN_ICONS = [
  'globe', 'server', 'database', 'cloud', 'home', 'chart', 'code', 'terminal', 'box', 'shield', 'mail', 'book',
  'film', 'music', 'photo', 'camera', 'download', 'chat', 'calendar', 'wrench', 'bolt', 'link',
] as const;

/** The Icon.svelte name for a tile's built-in icon, or null. */
export function builtinName(icon: string): string | null {
  const name = icon.startsWith('builtin:') ? icon.slice(8) : '';
  return (BUILTIN_ICONS as readonly string[]).includes(name) ? name : null;
}

/** Why a URL would be refused (mirrors internal/homepage ValidURL), or null. */
export function urlProblem(s: string): 'empty' | 'scheme' | 'credentials' | null {
  const v = s.trim();
  if (v === '') return 'empty';
  if (/\s/.test(v)) return 'scheme';
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    return 'scheme';
  }
  if ((u.protocol !== 'http:' && u.protocol !== 'https:') || u.hostname === '') return 'scheme';
  if (u.username !== '' || u.password !== '') return 'credentials';
  return null;
}

/** The port a URL leads to, to start the TCP check's field from; 0 if it is not a URL. */
export function portOf(s: string): number {
  try {
    const u = new URL(s.trim());
    return Number(u.port) || (u.protocol === 'https:' ? 443 : 80);
  } catch {
    return 0;
  }
}

/** A TCP check's port (mirrors internal/homepage tcpTarget). */
export function validPort(n: number | null | undefined): n is number {
  return typeof n === 'number' && Number.isInteger(n) && n >= 1 && n <= 65535;
}

/** "nas.local:5000" from a URL, for a tile's subtitle. */
export function hostOf(s: string): string {
  try {
    return new URL(s).host;
  } catch {
    return s;
  }
}

/** Tiles grouped in page order; groups appear where their first tile is. Ungrouped tiles form group "". */
export function groupTiles(tiles: Tile[]): { group: string; tiles: Tile[] }[] {
  const out: { group: string; tiles: Tile[] }[] = [];
  const at = new Map<string, number>();
  for (const t of [...tiles].sort((a, b) => a.position - b.position || a.id - b.id)) {
    let i = at.get(t.group);
    if (i === undefined) {
      i = out.length;
      at.set(t.group, i);
      out.push({ group: t.group, tiles: [] });
    }
    out[i]?.tiles.push(t);
  }
  return out;
}

/** The order after moving tile id one step (-1 earlier, +1 later) among all tiles. */
export function moved(tiles: Tile[], id: number, by: -1 | 1): number[] {
  const ids = [...tiles].sort((a, b) => a.position - b.position || a.id - b.id).map((t) => t.id);
  const i = ids.indexOf(id);
  const j = i + by;
  if (i < 0 || j < 0 || j >= ids.length) return ids;
  [ids[i], ids[j]] = [ids[j] as number, ids[i] as number];
  return ids;
}

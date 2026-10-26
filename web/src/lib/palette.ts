// Command palette (Ctrl+K): entries for pages, apps and units, and the
// ranking used to find them.

import type { App } from './apps';
import type { Unit } from './services';

export type EntryKind = 'page' | 'app' | 'unit';

export interface Entry {
  kind: EntryKind;
  id: string; // route id, app id, or "<scope>/<unit>"
  title: string;
  detail: string; // second line: description, generic name...
  keywords: string; // extra searchable text
  weight?: number; // breaks ties between equal matches, lower first
}

/** Most entries shown at once. */
export const MAX_RESULTS = 40;

export function pageEntries(pages: { id: string; title: string }[]): Entry[] {
  return pages.map((p) => ({ kind: 'page', id: p.id, title: p.title, detail: '', keywords: p.id }));
}

export function appEntries(apps: App[]): Entry[] {
  return apps.map((a) => ({
    kind: 'app',
    id: a.id,
    title: a.name,
    detail: a.generic_name ?? a.comment ?? '',
    keywords: [a.id, ...(a.keywords ?? [])].join(' '),
  }));
}

// Unit types worth jumping to, most wanted first (devices, slices and
// scopes are left out).
const unitTypes = ['service', 'timer', 'socket', 'mount', 'target', 'path'];

export function unitEntries(units: Unit[]): Entry[] {
  const out: Entry[] = [];
  for (const u of units) {
    const weight = unitTypes.indexOf(u.name.slice(u.name.lastIndexOf('.') + 1));
    if (weight >= 0) out.push({ kind: 'unit', id: `${u.scope}/${u.name}`, title: u.name, detail: u.description ?? '', keywords: '', weight });
  }
  return out;
}

/**
 * How well entry matches query (lower is better), or null for no match.
 * Title prefix < word start in title < substring of title < substring of
 * detail/keywords < letters of the query in order in the title, the
 * first of them starting a word (so "txe" finds "Text Editor" but "reso"
 * does not find "pipewire.socket").
 */
export function score(e: Entry, query: string): number | null {
  const q = query.trim().toLowerCase();
  if (q === '') return 0;
  const title = e.title.toLowerCase();
  if (title.startsWith(q)) return 0;
  const at = title.indexOf(q);
  if (at > 0 && /[\s._@-]/.test(title[at - 1] ?? '')) return 1;
  if (at > 0) return 2;
  if (`${e.detail} ${e.keywords}`.toLowerCase().includes(q)) return 3;
  let i = 0;
  for (let j = 0; j < title.length; j++) {
    const c = title[j];
    if (i === 0 && (c !== q[0] || (j > 0 && !/[\s._@-]/.test(title[j - 1] ?? '')))) continue;
    if (c === q[i]) i++;
    if (i === q.length) return 4;
  }
  return null;
}

const kindOrder: Record<EntryKind, number> = { page: 0, app: 1, unit: 2 };

/** The best matches: by score, then pages before apps before units, then shorter titles. */
export function search(entries: Entry[], query: string, max = MAX_RESULTS): Entry[] {
  const hits: { e: Entry; s: number }[] = [];
  for (const e of entries) {
    const s = score(e, query);
    if (s !== null) hits.push({ e, s });
  }
  hits.sort((a, b) => a.s - b.s || kindOrder[a.e.kind] - kindOrder[b.e.kind] || (a.e.weight ?? 0) - (b.e.weight ?? 0) || a.e.title.length - b.e.title.length || (a.e.title < b.e.title ? -1 : 1));
  return hits.slice(0, max).map((h) => h.e);
}

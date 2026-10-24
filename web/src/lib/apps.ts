// Applications page: types and filtering (internal/discovery).

import type { MessageKey } from './i18n';

export type Source = 'system' | 'user' | 'flatpak' | 'snap' | 'appimage';

export interface App {
  id: string;
  name: string;
  generic_name?: string;
  comment?: string;
  categories: string[];
  keywords?: string[];
  source: Source;
  path: string;
  terminal?: boolean;
  has_icon: boolean;
}

export type SourceFilter = 'all' | Source;

export const SOURCES: { id: SourceFilter; label: MessageKey }[] = [
  { id: 'all', label: 'apps2.source.all' },
  { id: 'system', label: 'apps2.source.system' },
  { id: 'user', label: 'apps2.source.user' },
  { id: 'flatpak', label: 'apps2.source.flatpak' },
  { id: 'snap', label: 'apps2.source.snap' },
  { id: 'appimage', label: 'apps2.source.appimage' },
];

// The freedesktop.org main categories.
export const CATEGORIES: { id: string; label: MessageKey }[] = [
  { id: 'AudioVideo', label: 'cat.AudioVideo' },
  { id: 'Development', label: 'cat.Development' },
  { id: 'Education', label: 'cat.Education' },
  { id: 'Game', label: 'cat.Game' },
  { id: 'Graphics', label: 'cat.Graphics' },
  { id: 'Network', label: 'cat.Network' },
  { id: 'Office', label: 'cat.Office' },
  { id: 'Science', label: 'cat.Science' },
  { id: 'Settings', label: 'cat.Settings' },
  { id: 'System', label: 'cat.System' },
  { id: 'Utility', label: 'cat.Utility' },
];

/** Apps matching source, category ('' = any) and search text, in list order. */
export function filterApps(apps: App[], source: SourceFilter, category: string, search: string): App[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return apps.filter((a) => {
    if (source !== 'all' && a.source !== source) return false;
    if (category !== '' && !a.categories.includes(category)) return false;
    if (words.length === 0) return true;
    const hay = [a.name, a.generic_name ?? '', a.comment ?? '', a.id, ...(a.keywords ?? []), ...a.categories].join(' ').toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

/** Search hits ranked: name starts with the query, then name contains it, then the rest. */
export function rank(apps: App[], search: string): App[] {
  const q = search.trim().toLowerCase();
  if (q === '') return apps;
  const score = (a: App) => {
    const n = a.name.toLowerCase();
    return n.startsWith(q) ? 0 : n.includes(q) ? 1 : 2;
  };
  return [...apps].sort((a, b) => score(a) - score(b));
}

/** A one- or two-letter placeholder for apps without an icon. */
export function initials(name: string): string {
  const parts = name.split(/[\s._-]+/).filter(Boolean);
  const letters = parts.length > 1 ? (parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '') : name.slice(0, 2);
  return letters.toUpperCase();
}

/** Query string for GET /apps/icons. */
export function iconQuery(ids: string[]): string {
  const q = new URLSearchParams();
  for (const id of ids) q.append('id', id);
  return q.toString();
}

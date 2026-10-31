// Tools page: the programs in PATH (internal/discovery tools.go). Nothing
// listed is ever run; owner and version come from files.

import type { Manager } from './packages';

export interface Tool {
  name: string;
  path: string;
  target?: string;
  alternative?: boolean;
  manager?: Manager;
  package?: string;
  package_id?: string;
  version?: string;
  summary?: string;
  shadowed?: string[];
}

export interface ToolList {
  dirs: string[];
  tools: Tool[];
}

export type SourceFilter = 'all' | 'apt' | 'other' | 'none';
export type SortKey = 'name' | 'package';

/** Tools matching the source filter and every search word, sorted. */
export function view(tools: Tool[], source: SourceFilter, search: string, sort: SortKey, desc: boolean): Tool[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const out = tools.filter((x) => {
    if (source === 'apt' && x.manager !== 'apt') return false;
    if (source === 'other' && (!x.manager || x.manager === 'apt')) return false;
    if (source === 'none' && x.manager) return false;
    if (words.length === 0) return true;
    const hay = `${x.name} ${x.package ?? ''} ${x.summary ?? ''}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  const dir = desc ? -1 : 1;
  const key = (x: Tool) => (sort === 'package' ? `${x.package ?? '￿'}\u0000${x.name}` : x.name).toLowerCase();
  return out.sort((a, b) => {
    const ka = key(a);
    const kb = key(b);
    return ka < kb ? -dir : ka > kb ? dir : 0;
  });
}

/** Counts per source filter, for the segmented control. */
export function sourceCounts(tools: Tool[]): Record<SourceFilter, number> {
  const c = { all: tools.length, apt: 0, other: 0, none: 0 };
  for (const x of tools) {
    if (x.manager === 'apt') c.apt++;
    else if (x.manager) c.other++;
    else c.none++;
  }
  return c;
}

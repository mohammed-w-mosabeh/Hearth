// Sorting and filtering of process groups for the Apps table.

import type { GroupKind, ProcGroup } from './types';

export type SortKey = 'name' | 'cpu' | 'mem' | 'read' | 'write' | 'count';
export type KindFilter = 'all' | 'apps' | 'services' | 'containers' | 'other';

const kinds: Record<Exclude<KindFilter, 'all'>, GroupKind[]> = {
  apps: ['app'],
  services: ['service', 'user-service'],
  containers: ['container'],
  other: ['session', 'kernel'],
};

function value(g: ProcGroup, k: SortKey): number | string {
  switch (k) {
    case 'name':
      return g.name.toLowerCase();
    case 'cpu':
      return g.cpu;
    case 'mem':
      return g.mem;
    case 'read':
      return g.read_bps ?? -1;
    case 'write':
      return g.write_bps ?? -1;
    case 'count':
      return g.count;
  }
}

/** Filters by kind and search text (name, unit, user, process names), then sorts. */
export function view(groups: ProcGroup[], kind: KindFilter, search: string, key: SortKey, desc: boolean): ProcGroup[] {
  const q = search.trim().toLowerCase();
  const out = groups.filter(
    (g) =>
      (kind === 'all' || kinds[kind].includes(g.kind)) &&
      (q === '' ||
        g.name.toLowerCase().includes(q) ||
        (g.unit ?? '').toLowerCase().includes(q) ||
        g.user.toLowerCase().includes(q) ||
        g.procs.some((p) => p.name.toLowerCase().includes(q) || String(p.pid) === q)),
  );
  const dir = desc ? -1 : 1;
  return out.sort((a, b) => {
    const va = value(a, key);
    const vb = value(b, key);
    if (va < vb) return -dir;
    if (va > vb) return dir;
    return a.id < b.id ? -1 : 1;
  });
}

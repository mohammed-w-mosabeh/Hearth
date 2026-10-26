// Services page: types, filtering and sorting of systemd units
// (internal/services, topic "services").

export type Scope = 'system' | 'user';

export interface Unit {
  name: string;
  scope: Scope;
  description?: string;
  load: string;
  active: string;
  sub: string;
  enabled?: string;
  job?: string;
  critical?: boolean;
}

export interface Stat {
  mem: number;
  cpu: number;
}

export type ServicesMsg =
  | { kind: 'list'; units: Unit[]; errors?: Partial<Record<Scope, string>> }
  | { kind: 'stats'; stats: Record<string, Stat> };

export type ScopeFilter = 'all' | Scope;
export type TypeFilter = 'service' | 'timer' | 'socket' | 'mount' | 'other' | 'all';
export type StateFilter = 'all' | 'running' | 'failed' | 'inactive';
export type SortKey = 'name' | 'state' | 'mem' | 'cpu';

/** The key stats use: "<scope>/<name>". */
export function key(u: Unit): string {
  return `${u.scope}/${u.name}`;
}

/** The unit type: "cups.service" -> "service". */
export function unitType(name: string): string {
  const i = name.lastIndexOf('.');
  return i < 0 ? '' : name.slice(i + 1);
}

const main = new Set(['service', 'timer', 'socket', 'mount']);

/** One word for the dot and the state filter. */
export function health(u: Unit): 'failed' | 'running' | 'changing' | 'inactive' {
  if (u.active === 'failed') return 'failed';
  if (u.active === 'active' || u.active === 'reloading') return 'running';
  if (u.active === 'activating' || u.active === 'deactivating' || u.active === 'refreshing') return 'changing';
  return 'inactive';
}

const healthOrder = { failed: 0, changing: 1, running: 2, inactive: 3 };

export interface Filters {
  scope: ScopeFilter;
  type: TypeFilter;
  state: StateFilter;
  search: string;
}

/** Units matching the filters, sorted; ties by name then scope. */
export function view(units: Unit[], stats: Record<string, Stat>, f: Filters, sort: SortKey, desc: boolean): Unit[] {
  const words = f.search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const out = units.filter((u) => {
    if (f.scope !== 'all' && u.scope !== f.scope) return false;
    const type = unitType(u.name);
    if (f.type === 'other' ? main.has(type) : f.type !== 'all' && type !== f.type) return false;
    const h = health(u);
    if (f.state === 'running' && h !== 'running' && h !== 'changing') return false;
    if (f.state === 'failed' && h !== 'failed') return false;
    if (f.state === 'inactive' && h !== 'inactive') return false;
    if (words.length === 0) return true;
    const hay = `${u.name} ${u.description ?? ''}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  const dir = desc ? -1 : 1;
  const value = (u: Unit): number | string => {
    switch (sort) {
      case 'name':
        return u.name.toLowerCase();
      case 'state':
        return healthOrder[health(u)];
      case 'mem':
        return stats[key(u)]?.mem ?? -1;
      case 'cpu':
        return stats[key(u)]?.cpu ?? -1;
    }
  };
  return out.sort((a, b) => {
    const va = value(a);
    const vb = value(b);
    if (va < vb) return -dir;
    if (va > vb) return dir;
    if (a.name !== b.name) return a.name < b.name ? -1 : 1;
    return a.scope < b.scope ? -1 : a.scope > b.scope ? 1 : 0;
  });
}

/** Failed units, for the banner. */
export function failedCount(units: Unit[]): number {
  return units.filter((u) => u.active === 'failed').length;
}

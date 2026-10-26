// Disks page: the folder size explorer (internal/diskusage) and helpers.

export interface UsageEntry {
  name: string;
  size: number;
  files: number;
  dir?: boolean;
  link?: boolean;
  other_fs?: boolean;
  pending?: boolean;
  nav: boolean;
}

export interface Usage {
  path: string;
  root: string;
  size: number;
  files: number;
  items: UsageEntry[];
  rest?: UsageEntry;
  unreadable: number;
  running?: boolean;
  partial?: boolean;
  done: number;
  total: number;
  took_ms: number;
  cached?: boolean;
  at: number;
}

export interface Crumb {
  name: string;
  path: string;
}

/** Breadcrumbs from root down to path: root first, shown by its full path. */
export function crumbs(path: string, root: string): Crumb[] {
  const out: Crumb[] = [{ name: root, path: root }];
  if (path === root || !path.startsWith(`${root}/`)) return out;
  let cur = root === '/' ? '' : root;
  for (const part of path.slice(root.length + 1).split('/')) {
    if (!part) continue;
    cur = `${cur}/${part}`;
    out.push({ name: part, path: cur });
  }
  return out;
}

/** The child folder's path. */
export function child(path: string, name: string): string {
  return path === '/' ? `/${name}` : `${path}/${name}`;
}

/** The explorable folder for a mount: the home if it lives on that mount, the mount itself if it is a root. */
export function exploreRoot(mount: string, roots: string[], mounts: string[]): string | null {
  const home = roots[0];
  if (home && mountOf(home, mounts) === mount) return home;
  return roots.includes(mount) ? mount : null;
}

/** Which mount holds path: the longest mount point that is a prefix. */
export function mountOf(path: string, mounts: string[]): string {
  let best = '/';
  for (const m of mounts) {
    if ((path === m || path.startsWith(m === '/' ? '/' : `${m}/`)) && m.length > best.length) best = m;
  }
  return best;
}

/** "usage.pct" class: ok, warn (>= 80%), crit (>= 90%). */
export function level(pct: number): 'ok' | 'warn' | 'crit' {
  return pct >= 90 ? 'crit' : pct >= 80 ? 'warn' : 'ok';
}

/** Query string for GET /disks/usage. */
export function usageQuery(path: string, fresh: boolean): string {
  const q = new URLSearchParams({ path });
  if (fresh) q.set('fresh', '1');
  return q.toString();
}

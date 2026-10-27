// Packages page: one list across apt/dpkg and the other managers
// (internal/discovery dpkg.go and extra.go, internal/pkgs).

import { reasonKey } from './actions';
import type { MessageKey } from './i18n';

export type Manager = 'apt' | 'flatpak' | 'snap' | 'pip' | 'npm' | 'cargo' | 'go';
export const extraManagers: Exclude<Manager, 'apt'>[] = ['flatpak', 'snap', 'pip', 'npm', 'cargo', 'go'];

/** GET /api/v1/pkgs: one dpkg package. */
export interface DebPackage {
  name: string;
  arch: string;
  version: string;
  state: string;
  hold?: boolean;
  auto?: boolean;
  essential?: boolean;
  protected?: boolean;
  priority?: string;
  section?: string;
  installed_kib?: number;
  source?: string;
  maintainer?: string;
  summary?: string;
  depends?: string;
}

/** GET /api/v1/pkgs/apt/{id}. */
export interface DebDetail extends DebPackage {
  deps: string[];
  reverse_deps: string[];
  files: string[];
  files_truncated?: boolean;
}

/** GET /api/v1/pkgs/{manager}: one package of another manager. */
export interface ExtraPackage {
  id: string;
  name: string;
  version?: string;
  summary?: string;
  kind?: string;
  location?: string;
  size_bytes?: number;
  used_by?: string[];
  removable: boolean;
  why?: string;
}

export interface ExtraList {
  manager: string;
  available: boolean;
  packages: ExtraPackage[];
}

/** GET /api/v1/pkgs/updates: one available update. */
export interface Update {
  name: string;
  arch: string;
  version: string;
  severity: string;
  security: boolean;
  summary?: string;
}

/** One row of the page, whatever the manager. */
export interface Row {
  key: string; // "<manager>/<id>"
  manager: Manager;
  id: string; // the action target
  name: string;
  version: string;
  summary: string;
  size: number; // bytes, -1 if unknown
  location: string;
  auto: boolean;
  held: boolean;
  update?: Update;
  removable: boolean;
  why?: string; // reason code when not removable
  usedBy?: string[];
}

/** Why removing a dpkg package is refused (internal/pkgs undeletableReason). */
function debWhy(p: DebPackage): string | undefined {
  if (p.name === 'hearth') return 'self';
  if (p.essential) return 'essential';
  if (p.protected) return 'protected';
  if (p.priority === 'required') return 'required';
  return undefined;
}

/** Rows for installed dpkg packages, with their updates. */
export function debRows(pkgs: DebPackage[], updates: Update[]): Row[] {
  const ups = new Map(updates.map((u) => [`${u.name}:${u.arch}`, u]));
  const multi = new Set<string>();
  const seen = new Set<string>();
  for (const p of pkgs) {
    if (seen.has(p.name)) multi.add(p.name);
    seen.add(p.name);
  }
  return pkgs
    .filter((p) => p.state === 'installed')
    .map((p) => {
      const why = debWhy(p);
      const update = ups.get(`${p.name}:${p.arch}`);
      return {
        key: `apt/${p.name}:${p.arch}`,
        manager: 'apt',
        // The architecture is needed only when two are installed.
        id: multi.has(p.name) ? `${p.name}:${p.arch}` : p.name,
        name: p.name,
        version: p.version,
        summary: p.summary ?? '',
        size: p.installed_kib ? p.installed_kib * 1024 : -1,
        location: p.section ?? '',
        auto: p.auto === true,
        held: p.hold === true,
        ...(update ? { update } : {}),
        removable: why === undefined,
        ...(why ? { why } : {}),
      } satisfies Row;
    });
}

/** Rows for one other manager. */
export function extraRows(manager: Exclude<Manager, 'apt'>, list: ExtraList): Row[] {
  return list.packages.map((p) => ({
    key: `${manager}/${p.id}`,
    manager,
    id: p.id,
    name: p.name,
    version: p.version ?? '',
    summary: p.summary ?? '',
    size: p.size_bytes ?? -1,
    location: p.location ?? '',
    auto: false,
    held: false,
    removable: p.removable,
    ...(p.why ? { why: p.why } : {}),
    ...(p.used_by?.length ? { usedBy: p.used_by } : {}),
  }));
}

/** The action kind that removes a row's package, or null (pip). */
export function removeKind(m: Manager): string | null {
  switch (m) {
    case 'apt':
      return 'package';
    case 'go':
      return 'gobin';
    case 'pip':
      return null;
    default:
      return m;
  }
}

export type ManagerFilter = 'all' | Manager;
export type ShowFilter = 'all' | 'updates' | 'manual' | 'held';
export type SortKey = 'name' | 'size' | 'manager';

export interface Filters {
  manager: ManagerFilter;
  show: ShowFilter;
  search: string;
}

const managerOrder: Record<Manager, number> = { apt: 0, flatpak: 1, snap: 2, pip: 3, npm: 4, cargo: 5, go: 6 };

/** Rows matching the filters, sorted; ties by name then key. */
export function view(rows: Row[], f: Filters, sort: SortKey, desc: boolean): Row[] {
  const words = f.search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const out = rows.filter((r) => {
    if (f.manager !== 'all' && r.manager !== f.manager) return false;
    if (f.show === 'updates' && !r.update) return false;
    if (f.show === 'manual' && r.auto) return false;
    if (f.show === 'held' && !r.held) return false;
    if (words.length === 0) return true;
    const hay = `${r.name} ${r.summary}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  const dir = desc ? -1 : 1;
  const value = (r: Row): number | string => {
    switch (sort) {
      case 'name':
        return r.name.toLowerCase();
      case 'size':
        return r.size;
      case 'manager':
        return managerOrder[r.manager];
    }
  };
  return out.sort((a, b) => {
    const va = value(a);
    const vb = value(b);
    if (va < vb) return -dir;
    if (va > vb) return dir;
    const na = a.name.toLowerCase();
    const nb = b.name.toLowerCase();
    if (na !== nb) return na < nb ? -1 : 1;
    return a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
  });
}

/** Counts for the update banner: all applicable updates, and security ones. */
export function updateCounts(updates: Update[]): { all: number; security: number } {
  const ok = updates.filter((u) => u.severity !== 'blocked');
  return { all: ok.length, security: ok.filter((u) => u.security).length };
}

/** Rows per manager, for the tab labels. */
export function counts(rows: Row[]): Partial<Record<Manager, number>> {
  const out: Partial<Record<Manager, number>> = {};
  for (const r of rows) out[r.manager] = (out[r.manager] ?? 0) + 1;
  return out;
}

/** The i18n key for a blocked/failed reason, in package words. */
export function pkgReasonKey(code: string): MessageKey {
  if (code === 'in-use') return 'pkg.reason.inUse';
  if (code === 'not-found') return 'pkg.reason.notFound';
  if (code === 'system') return 'pkg.reason.systemSnap';
  return reasonKey(code);
}

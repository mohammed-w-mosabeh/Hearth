// Space advisor page: types of internal/space and helpers to show them.

import type { Params } from './actions';

export const MAX_SELECTED = 16;

export interface Entry {
  name: string;
  path: string;
  size: number;
  time?: number;
}

export interface Group {
  size: number;
  count: number;
  items?: Entry[];
}

export interface Kernel {
  package: string;
  kernel: string;
  size: number;
}

export interface Report {
  apt: Group;
  journal: Group;
  cache: Group;
  trash: Group;
  kernels: Kernel[];
  running: string;
  can_trash: boolean;
  journal_keep: number[];
}

export interface Large {
  items: Entry[];
  truncated: boolean;
}

/** What the journal could give back when keeping keepMiB. */
export function journalFreed(size: number, keepMiB: number): number {
  return Math.max(0, size - keepMiB * 1024 * 1024);
}

/** The total that could be freed without deciding about personal files. */
export function freeable(r: Report, keepMiB: number, docker: number): number {
  const kernels = r.kernels.reduce((sum, k) => sum + k.size, 0);
  return r.apt.size + journalFreed(r.journal.size, keepMiB) + kernels + r.cache.size + r.trash.size + docker;
}

/** Selected paths as the params of space/trash. */
export function pathParams(paths: readonly string[]): Params {
  return Object.fromEntries(paths.map((p, i) => [`p${String(i).padStart(2, '0')}`, p]));
}

export function toggled(list: readonly string[], v: string): string[] {
  if (list.includes(v)) return list.filter((x) => x !== v);
  return list.length < MAX_SELECTED ? [...list, v] : [...list];
}

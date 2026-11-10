// Uninstall completely (ROADMAP 11.9, SECURITY S16k): the plan as the
// daemon builds it, the selection the page keeps, and what must be typed.

export type Group = 'program' | 'orphans' | 'purge' | 'running' | 'user' | 'system' | 'refs' | 'remains';

/** The preview's groups, in order. */
export const GROUPS: Group[] = ['program', 'orphans', 'purge', 'running', 'user', 'system', 'refs', 'remains'];

/** Must be typed to remove a desktop or display package. */
export const DESKTOP_WORDS = 'remove my desktop';

export interface Candidate {
  group: Group;
  kind: string;
  id: string;
  name: string;
  size?: number;
  preselect: boolean;
  reason: string;
  warn?: string; // data, desktop, large, blocks
}

export interface Owner {
  manager: string;
  target: string;
  name: string;
  packages?: string[];
  paths?: string[];
}

export interface UninstallPlan {
  item: string;
  name: string;
  owner: Owner;
  candidates: Candidate[];
  blocked?: string;
  desktop?: string[];
  freed: number;
}

export interface Step {
  step: string;
  id: string;
  name: string;
  done: boolean;
  detail?: string;
}

export interface RestoreRow {
  kind: string;
  id: string;
  name: string;
  detail?: string;
  ok: boolean;
  done: boolean;
}

/** One operation of Recently removed. */
export interface Operation {
  id: string;
  item: string;
  name: string;
  created: number;
  state: 'running' | 'done' | 'failed' | 'restored';
  failed?: string;
  rows: Candidate[];
  steps: Step[];
  freed: number;
  restore?: RestoreRow[];
}

/** A candidate's key in a selection. */
export const keyOf = (c: Candidate): string => `${c.group}|${c.id}`;

/** Rows that always go with the program: its packages and what they take
 *  along cannot be left out. */
export const fixed = (c: Candidate): boolean => c.group === 'program' && c.kind === 'package';

/** Rows the user may tick at all. */
export const selectable = (c: Candidate): boolean => c.group !== 'remains';

/** The rows ticked by default. */
export function defaults(p: UninstallPlan): Set<string> {
  return new Set(p.candidates.filter((c) => selectable(c) && (c.preselect || fixed(c))).map(keyOf));
}

/** The space the ticked rows free (configuration purges count nothing). */
export function freed(p: UninstallPlan, sel: ReadonlySet<string>): number {
  let n = 0;
  for (const c of p.candidates) if (sel.has(keyOf(c)) && c.group !== 'purge') n += c.size ?? 0;
  return n;
}

/** What must be typed before the run: the desktop words, each data path. */
export function needed(p: UninstallPlan, sel: ReadonlySet<string>): { desktop: boolean; data: string[] } {
  return {
    desktop: (p.desktop ?? []).length > 0,
    data: p.candidates.filter((c) => c.warn === 'data' && sel.has(keyOf(c))).map((c) => c.id),
  };
}

/** Whether every typed word matches. */
export function typedOk(p: UninstallPlan, sel: ReadonlySet<string>, desktop: string, data: Readonly<Record<string, string>>): boolean {
  const n = needed(p, sel);
  if (n.desktop && desktop !== DESKTOP_WORDS) return false;
  return n.data.every((path) => data[path] === path);
}

/** The plan's rows by group, in GROUPS order, empty groups left out. */
export function grouped(p: UninstallPlan): [Group, Candidate[]][] {
  const out: [Group, Candidate[]][] = [];
  for (const g of GROUPS) {
    const rows = p.candidates.filter((c) => c.group === g);
    if (rows.length) out.push([g, rows]);
  }
  return out;
}

/** The action's params for a selection. */
export function params(p: UninstallPlan, sel: ReadonlySet<string>, desktop: string, data: Readonly<Record<string, string>>): Record<string, string> {
  const out: Record<string, string> = { select: JSON.stringify([...sel].filter((k) => p.candidates.some((c) => keyOf(c) === k && selectable(c)))) };
  const n = needed(p, sel);
  if (n.desktop) out.desktop = desktop;
  if (n.data.length) out.data = JSON.stringify(n.data.map((path) => data[path] ?? ''));
  return out;
}

const extraItem: Readonly<Record<string, string>> = { flatpak: 'flatpak', snap: 'snap', npm: 'npm', cargo: 'cargo', go: 'gobin', pip: 'pip' };

/** The uninstall item of a Packages row: "package:<id>" for apt, else
 *  "<manager>:<id>"; null for an unknown manager. */
export function itemFor(manager: string, id: string): string | null {
  if (manager === 'apt') return `package:${id}`;
  const k = extraItem[manager];
  return k ? `${k}:${id}` : null;
}

/** The page that uninstalls item completely. */
export const uninstallHref = (item: string): string => `#/removed?item=${encodeURIComponent(item)}`;

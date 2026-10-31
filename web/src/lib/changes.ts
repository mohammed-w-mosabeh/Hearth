// Changes page: types of internal/changes and helpers to show them.

import type { MessageKey } from './i18n';

export const SOURCES = ['packages', 'units', 'containers', 'listeners', 'hearth'] as const;
export const KINDS = ['installed', 'upgraded', 'removed', 'purged', 'added', 'deleted', 'changed'] as const;

export interface TimelineEvent {
  t: string;
  source: string;
  kind: string;
  subject: string;
  before?: string;
  after?: string;
  action?: string;
  origin?: string;
  result?: string;
}

export interface Timeline {
  events: TimelineEvent[];
  next?: number;
}

export interface Snapshot {
  id: number;
  t: string;
  label: string;
  auto: boolean;
  size: number;
}

export interface Entry {
  key: string;
  before?: string;
  after?: string;
}

export interface SectionDiff {
  source: string;
  added: Entry[];
  removed: Entry[];
  changed: Entry[];
  missing?: boolean;
}

export interface SnapshotDiff {
  from: string;
  to: string;
  sections: SectionDiff[];
}

export interface DiffRow extends Entry {
  mark: 'added' | 'removed' | 'changed';
}

export interface DayGroup {
  day: string;
  events: TimelineEvent[];
}

function known(list: readonly string[], v: string): boolean {
  return list.includes(v);
}

/** The plural name of a source ("Packages"), for filters and sections. */
export function sourceKey(source: string): MessageKey {
  return known(SOURCES, source) ? (`changes.source.${source}` as MessageKey) : 'changes.source.other';
}

/** The message for an event; Hearth actions show their own name. */
export function eventKey(e: TimelineEvent): MessageKey | null {
  if (e.source === 'hearth' || !known(SOURCES, e.source) || !known(KINDS, e.kind)) return null;
  return `changes.ev.${e.source}.${e.kind}` as MessageKey;
}

/** "1.0 → 1.1", or the one value there is. */
export function entryDetail(e: Pick<Entry, 'before' | 'after'>): string {
  if (e.before && e.after) return `${e.before} → ${e.after}`;
  return e.before || e.after || '';
}

/** An entry's detail, or how a Hearth action went and from where. */
export function eventDetail(e: TimelineEvent): string {
  if (e.source === 'hearth') return `${e.result ?? ''} (${e.origin ?? ''})`;
  return entryDetail(e);
}

/** The path for a page of the timeline; no filter or all of them is everything. */
export function timelineURL(sources: readonly string[], before?: number): string {
  const q = new URLSearchParams();
  if (before !== undefined) q.set('before', String(before));
  if (sources.length > 0 && sources.length < SOURCES.length) q.set('source', sources.join(','));
  const s = q.toString();
  return s ? `/changes?${s}` : '/changes';
}

export function toggled(list: readonly string[], v: string): string[] {
  return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
}

/** Events (newest first) by local day, in order. */
export function byDay(events: readonly TimelineEvent[], day: (iso: string) => string): DayGroup[] {
  const out: DayGroup[] = [];
  for (const e of events) {
    const d = day(e.t);
    const last = out[out.length - 1];
    if (last?.day === d) last.events.push(e);
    else out.push({ day: d, events: [e] });
  }
  return out;
}

/** One list of a section's changes, added then removed then changed. */
export function diffRows(s: SectionDiff): DiffRow[] {
  return [
    ...s.added.map((e): DiffRow => ({ ...e, mark: 'added' })),
    ...s.removed.map((e): DiffRow => ({ ...e, mark: 'removed' })),
    ...s.changed.map((e): DiffRow => ({ ...e, mark: 'changed' })),
  ];
}

export function diffEmpty(sections: readonly SectionDiff[]): boolean {
  return sections.every((s) => !s.missing && diffRows(s).length === 0);
}

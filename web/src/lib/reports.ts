// Reports page: types of internal/reports and helpers to show them.

import type { Unit } from './charts';
import type { Columns } from './history';
import type { MessageKey } from './i18n';

export type Period = 'day' | 'week' | 'month';
export type Format = 'html' | 'md';

export const PERIODS: { id: Period; label: MessageKey }[] = [
  { id: 'day', label: 'reports.period.day' },
  { id: 'week', label: 'reports.period.week' },
  { id: 'month', label: 'reports.period.month' },
];

export interface Stat {
  metric: string;
  label: string;
  unit: string; // "%", "bytes", "bytes/s", "°C" or ""
  avg: number;
  max: number;
  total?: number;
}

export interface Trend {
  metric: string;
  label: string;
  unit: string;
  peak: number;
  max: number;
  t: number[];
  v: (number | null)[];
}

export interface ItemTotal {
  item: string;
  kind: string;
  name: string;
  cpu_avg: number;
  cpu_max: number;
  busy_hours: number;
  mem_avg: number;
  mem_max: number;
  hours: number;
}

export interface ItemHour {
  item: string;
  kind: string;
  name: string;
  t: string;
  cpu_avg: number;
  cpu_max: number;
  mem_avg: number;
  mem_max: number;
  n: number;
}

export interface AlertEvent {
  id: number;
  rule_name: string;
  metric: string;
  instance: string;
  severity: string;
  peak: number;
  started_at: number;
  ended_at?: number;
}

export interface Change {
  t: string;
  origin: string;
  action: string;
  target: string;
  result: string;
}

export interface Disk {
  mount: string;
  total: number;
  used: number;
  pct: number;
  growth?: number;
}

export interface Battery {
  id: string;
  model?: string;
  pct: number;
  health_pct: number;
  cycles?: number;
}

export interface MachineReport {
  host: string;
  period: Period;
  from: string;
  to: string;
  generated: string;
  stats: Stat[];
  trends: Trend[];
  top: ItemTotal[];
  alerts: AlertEvent[];
  changes: Change[];
  disks: Disk[];
  batteries: Battery[];
  covered: number;
}

export interface ItemReport {
  id: string;
  kind: string;
  name: string;
  period: Period;
  from: string;
  to: string;
  total: ItemTotal | null;
  hours: ItemHour[];
  trends: Trend[];
  peak_cpu?: ItemHour;
  peak_mem?: ItemHour;
  events: AlertEvent[];
  changes: Change[];
}

export interface Saved {
  name: string;
  time: number;
  size: number;
}

export interface WeeklyState {
  enabled: boolean;
  last?: number;
  saved?: Saved[];
}

/** The chart unit of a report unit. */
export function chartUnit(unit: string): Unit {
  switch (unit) {
    case '%':
      return 'pct';
    case 'bytes':
      return 'bytes';
    case 'bytes/s':
      return 'rate';
    case '°C':
      return 'c';
  }
  return 'load';
}

/** A trend as chart columns (one series, keyed by its metric). */
export function trendColumns(tr: Trend): Columns {
  return { t: tr.t, series: { [tr.metric]: tr.v } };
}

/** Whether a trend has enough points for a line (uPlot draws no lone point). */
export function drawable(tr: Trend): boolean {
  return tr.v.filter((v) => v !== null).length >= 2;
}

/** The x range of a report's charts, in unix seconds. */
export function xRange(from: string, to: string): [number, number] {
  return [Date.parse(from) / 1000, Date.parse(to) / 1000];
}

const KINDS: Record<string, MessageKey> = {
  app: 'reports.kind.app',
  service: 'reports.kind.service',
  'user-service': 'reports.kind.userService',
  container: 'reports.kind.container',
};

export function kindLabel(kind: string): MessageKey {
  return KINDS[kind] ?? 'reports.kind.other';
}

/** CPU time from hours of the whole machine, in seconds (for duration()). */
export function cpuSeconds(machineHours: number): number {
  return machineHours * 3600;
}

/** Report query for the server. */
export function reportQuery(period: Period, item?: string, format?: Format): string {
  const q = new URLSearchParams({ period });
  if (item !== undefined) q.set('id', item);
  if (format !== undefined) q.set('format', format);
  return q.toString();
}

/** Picker search over recorded items (name, kind, ID). */
export function filterItems(items: ItemTotal[], search: string): ItemTotal[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return items.filter((i) => {
    const hay = `${i.name} ${i.kind} ${i.item}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

/** The file name from a Content-Disposition header, if it is a plain one. */
export function attachmentName(header: string | null, fallback: string): string {
  const m = /filename="([A-Za-z0-9._-]{1,128})"/.exec(header ?? '');
  return m?.[1] ?? fallback;
}

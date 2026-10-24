// Metric history as columns, as /metrics/live and /metrics/history return
// it: t in unix ms, one value per t for each series (null = no data).

import type { MessageKey } from './i18n';

export interface Columns {
  t: number[];
  series: Record<string, (number | null)[]>;
  max?: Record<string, (number | null)[]>;
}

export type Range = '10m' | '24h' | '7d' | '1y';

export const RANGES: { id: Range; label: MessageKey; seconds: number }[] = [
  { id: '10m', label: 'range.10m', seconds: 600 },
  { id: '24h', label: 'range.24h', seconds: 86_400 },
  { id: '7d', label: 'range.7d', seconds: 7 * 86_400 },
  { id: '1y', label: 'range.1y', seconds: 365 * 86_400 },
];

/** Same rule as the server: a name, or a prefix ending in ":" or ".". */
export function matches(metric: string, sel: string[]): boolean {
  return sel.some((s) => metric === s || ((s.endsWith(':') || s.endsWith('.')) && metric.startsWith(s)));
}

/** Appends newer columns to base and drops points older than minT (ms). */
export function append(base: Columns, add: Columns, minT: number): Columns {
  const last = base.t.at(-1) ?? -Infinity;
  const from = add.t.findIndex((t) => t > last);
  const n = from < 0 ? 0 : add.t.length - from;
  const t = [...base.t, ...add.t.slice(from < 0 ? add.t.length : from)];
  const series: Columns['series'] = {};
  for (const k of new Set([...Object.keys(base.series), ...Object.keys(add.series)])) {
    const head = base.series[k] ?? new Array<null>(base.t.length).fill(null);
    const tail = add.series[k]?.slice(add.t.length - n) ?? new Array<null>(n).fill(null);
    series[k] = [...head, ...tail];
  }
  const drop = t.findIndex((x) => x >= minT);
  const cut = drop < 0 ? t.length : drop;
  for (const k of Object.keys(series)) series[k] = (series[k] ?? []).slice(cut);
  return { t: t.slice(cut), series };
}

/**
 * The non-empty series of cols selected by sel, in selector order, then
 * natural order within a prefix (core 2 before core 10).
 */
export function pick(cols: Columns, sel: string[]): string[] {
  const rank = (k: string) => sel.findIndex((s) => matches(k, [s]));
  return Object.keys(cols.series)
    .filter((k) => rank(k) >= 0 && (cols.series[k] ?? []).some((v) => v !== null))
    .sort((a, b) => rank(a) - rank(b) || a.localeCompare(b, 'en', { numeric: true }));
}

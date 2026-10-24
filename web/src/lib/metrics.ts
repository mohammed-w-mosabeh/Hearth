import type { LiveSeries, Sample } from './types';

// Live metrics for the Home cards: the latest sample plus short series for
// sparklines. The series are filled from the server's ring buffer first, so
// charts are not empty when the page opens, then grow from the WS topic.

export const WINDOW_S = 120;

// Series kept for sparklines. Names match the history metrics
// (collect.Sample.Points); prefixes ending in ":" cover one per device.
// net.rx / net.tx are totals over physical interfaces.
const SELECTORS = ['cpu.usage', 'mem.used', 'swap.used', 'cpu.temp', 'gpu.util:', 'battery.pct:', 'net.rx:', 'net.tx:'];

/** The sparkline values of one sample, keyed like the server's metrics. */
export function seriesValues(s: Sample): Map<string, number> {
  const out = new Map<string, number>();
  if (!s.partial) out.set('cpu.usage', s.cpu.usage);
  out.set('mem.used', s.mem.used);
  out.set('swap.used', s.mem.swap_used);
  if (s.cpu_temp_c !== undefined) out.set('cpu.temp', s.cpu_temp_c);
  for (const g of s.gpus ?? []) if (g.util_pct !== undefined) out.set(`gpu.util:${g.pci}`, g.util_pct);
  for (const b of s.power?.batteries ?? []) out.set(`battery.pct:${b.id}`, b.pct);
  if (!s.partial) {
    let rx = 0;
    let tx = 0;
    for (const n of s.net ?? []) {
      if (n.virtual) continue;
      rx += n.rx_bps;
      tx += n.tx_bps;
    }
    out.set('net.rx', rx);
    out.set('net.tx', tx);
  }
  return out;
}

/** Turns per-interface net.rx:/net.tx: columns into net.rx/net.tx totals. */
export function totalNet(live: LiveSeries): LiveSeries {
  const series: LiveSeries['series'] = {};
  const totals: Record<string, (number | null)[]> = {};
  for (const [name, col] of Object.entries(live.series)) {
    const total = /^(net\.(?:rx|tx)):/.exec(name)?.[1];
    if (total === undefined) {
      series[name] = col;
      continue;
    }
    const tot = (totals[total] ??= live.t.map(() => null));
    col.forEach((v, i) => {
      if (v !== null) tot[i] = (tot[i] ?? 0) + v;
    });
  }
  return { t: live.t, series: { ...series, ...totals } };
}

export class Window {
  t: number[] = []; // unix seconds
  cols = new Map<string, (number | null)[]>();

  push(tSec: number, values: Map<string, number>): void {
    const i = this.t.length;
    this.t.push(tSec);
    for (const [k, v] of values) {
      let col = this.cols.get(k);
      if (!col) {
        col = new Array<number | null>(i).fill(null);
        this.cols.set(k, col);
      }
      col.push(v);
    }
    for (const col of this.cols.values()) if (col.length === i) col.push(null);
    this.trim(tSec - WINDOW_S);
  }

  /** Puts older points from the server in front of what is already here. */
  prepend(live: LiveSeries): void {
    const first = this.t[0] ?? Infinity;
    const n = live.t.findIndex((ms) => ms / 1000 >= first);
    const count = n < 0 ? live.t.length : n;
    if (count === 0) return;
    const head = live.t.slice(0, count).map((ms) => ms / 1000);
    const len = this.t.length;
    for (const k of new Set([...this.cols.keys(), ...Object.keys(live.series)])) {
      const src = live.series[k]?.slice(0, count) ?? new Array<null>(count).fill(null);
      this.cols.set(k, [...src, ...(this.cols.get(k) ?? new Array<null>(len).fill(null))]);
    }
    this.t = [...head, ...this.t];
    this.trim((this.t.at(-1) ?? 0) - WINDOW_S);
  }

  /** [t, col...] in uPlot's aligned-data shape. */
  data(keys: string[]): [number[], ...(number | null)[][]] {
    return [this.t, ...keys.map((k) => this.cols.get(k) ?? this.t.map(() => null))];
  }

  private trim(minT: number): void {
    const n = this.t.findIndex((t) => t >= minT);
    const drop = n < 0 ? this.t.length : n;
    if (drop === 0) return;
    this.t = this.t.slice(drop);
    for (const [k, col] of this.cols) this.cols.set(k, col.slice(drop));
  }
}

/** Query string for GET /metrics/live covering the last WINDOW_S seconds. */
export function liveQuery(now: number): string {
  const q = new URLSearchParams();
  for (const m of SELECTORS) q.append('m', m);
  q.set('since', String(now - WINDOW_S * 1000));
  return q.toString();
}

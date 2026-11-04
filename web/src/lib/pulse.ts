import type { LiveSeries, Sample } from './types';

// The pulse strip's numbers (ROADMAP 8.5). It reads the sampler's ring every
// 15 s, which never speeds the sampler up; a page already at 1 s feeds it too.

export const PULSE_MS = 15_000;

export interface Pulse {
  cpu: number | null;
  memUsed: number | null;
  temp: number | null;
  down: number | null;
  up: number | null;
  battery: number | null;
}

export const emptyPulse: Pulse = { cpu: null, memUsed: null, temp: null, down: null, up: null, battery: null };

/** Where a click on each value leads: an id of Resources' charts. */
export const pulseChart = { cpu: 'cpu', ram: 'mem', temp: 'temps', net: 'net', battery: 'battery' } as const;

export function pulseQuery(now: number): string {
  const q = new URLSearchParams();
  for (const m of ['cpu.usage', 'mem.used', 'cpu.temp', 'temp:', 'net.rx:', 'net.tx:', 'battery.pct:']) q.append('m', m);
  q.set('since', String(now - 2 * PULSE_MS));
  return q.toString();
}

const last = (col: (number | null)[]): number | null => {
  for (let i = col.length - 1; i >= 0; i--) {
    const v = col[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
};

export function pulseFromSeries(live: LiveSeries): Pulse {
  const out = { ...emptyPulse };
  for (const [key, col] of Object.entries(live.series)) {
    const v = last(col);
    if (v === null) continue;
    if (key === 'cpu.usage') out.cpu = v;
    else if (key === 'mem.used') out.memUsed = v;
    else if (key === 'cpu.temp' || key.startsWith('temp:')) out.temp = Math.max(out.temp ?? v, v);
    else if (key.startsWith('net.rx:')) out.down = (out.down ?? 0) + v;
    else if (key.startsWith('net.tx:')) out.up = (out.up ?? 0) + v;
    else if (key.startsWith('battery.pct:')) out.battery ??= v;
  }
  return out;
}

export function pulseFromSample(s: Sample): Pulse {
  const temps = [...(s.temps ?? []).map((t) => t.c), ...(s.cpu_temp_c === undefined ? [] : [s.cpu_temp_c])];
  const nets = (s.net ?? []).filter((n) => !n.virtual);
  return {
    cpu: s.partial ? null : s.cpu.usage,
    memUsed: s.mem.used,
    temp: temps.length > 0 ? Math.max(...temps) : null,
    down: s.partial ? null : nets.reduce((sum, n) => sum + n.rx_bps, 0),
    up: s.partial ? null : nets.reduce((sum, n) => sum + n.tx_bps, 0),
    battery: s.power?.batteries?.[0]?.pct ?? null,
  };
}

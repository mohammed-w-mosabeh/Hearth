// Number formatting for the UI. Binary units (KiB, MiB) for sizes, decimal
// bits per second would confuse more than help, so rates are bytes/s too.

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

export function bytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '–';
  let i = 0;
  while (n >= 1024 && i < UNITS.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 || n >= 100 ? Math.round(n) : n.toFixed(1)} ${UNITS[i]}`;
}

export function rate(bps: number): string {
  return Number.isFinite(bps) && bps >= 0 ? `${bytes(bps)}/s` : '–';
}

export function pct(v: number, digits = 0): string {
  return Number.isFinite(v) ? `${v.toFixed(digits)}%` : '–';
}

export function celsius(v: number): string {
  return Number.isFinite(v) ? `${Math.round(v)}°C` : '–';
}

/** 8160 -> "2 h 16 min"; under a minute -> "< 1 min". */
export function duration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '–';
  const m = Math.floor(seconds / 60);
  if (m < 1) return '< 1 min';
  const h = Math.floor(m / 60);
  return h === 0 ? `${m} min` : `${h} h ${m % 60} min`;
}

export function mhz(v: number): string {
  return Number.isFinite(v) ? (v >= 1000 ? `${(v / 1000).toFixed(2)} GHz` : `${Math.round(v)} MHz`) : '–';
}

export function watts(v: number): string {
  return Number.isFinite(v) ? `${v.toFixed(v < 10 ? 1 : 0)} W` : '–';
}

export function rpm(v: number): string {
  return Number.isFinite(v) ? `${Math.round(v)} RPM` : '–';
}

// Number formatting for the UI. Binary units (KiB, MiB) for sizes, decimal
// bits per second would confuse more than help, so rates are bytes/s too.

import { ltr, t } from './i18n';

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

function size(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '–';
  let i = 0;
  while (n >= 1024 && i < UNITS.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 || n >= 100 ? Math.round(n) : n.toFixed(1)} ${UNITS[i]}`;
}

export const bytes = (n: number): string => ltr(size(n));

export function rate(bps: number): string {
  return Number.isFinite(bps) && bps >= 0 ? ltr(`${size(bps)}/s`) : '–';
}

export function pct(v: number, digits = 0): string {
  return Number.isFinite(v) ? ltr(`${v.toFixed(digits)}%`) : '–';
}

export function celsius(v: number): string {
  return Number.isFinite(v) ? ltr(`${Math.round(v)}°C`) : '–';
}

/** 8160 -> "2 h 16 min"; under a minute -> "< 1 min". */
export function duration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '–';
  const m = Math.floor(seconds / 60);
  if (m < 1) return t('fmt.underMin');
  const h = Math.floor(m / 60);
  return h === 0 ? t('fmt.min', { m }) : t('fmt.hmin', { h, m: m % 60 });
}

export function mhz(v: number): string {
  return Number.isFinite(v) ? ltr(v >= 1000 ? `${(v / 1000).toFixed(2)} GHz` : `${Math.round(v)} MHz`) : '–';
}

export function watts(v: number): string {
  return Number.isFinite(v) ? ltr(`${v.toFixed(v < 10 ? 1 : 0)} W`) : '–';
}

export function rpm(v: number): string {
  return Number.isFinite(v) ? ltr(`${Math.round(v)} RPM`) : '–';
}

// Heat and energy page: types of internal/insight and helpers to show them.

import type { MessageKey } from './i18n';

export const CAUSES = ['app', 'gpu', 'system', 'cooling', 'hot-idle', 'quiet'] as const;
export const PERIODS = ['day', 'week', 'month'] as const;
export type EnergyPeriod = (typeof PERIODS)[number];

export interface Cause {
  code: string;
  subject?: string;
  value?: number;
}

export interface Busy {
  id: string;
  kind: string;
  name: string;
  cpu: number;
}

export interface Gpu {
  name: string;
  util?: number;
  temp_c?: number;
  power_w?: number;
}

export interface Diagnosis {
  time: string;
  causes: Cause[];
  cpu: number;
  temp_c?: number;
  fans: { chip: string; label: string; rpm: number }[];
  gpus: Gpu[];
  top: Busy[];
}

export interface EnergyItem {
  item: string;
  kind: string;
  name: string;
  wh: number;
  share: number;
  hours: number;
}

export interface Energy {
  period: string;
  items: EnergyItem[];
  total_wh: number;
  full_watts: number;
  fitted: boolean;
  fit_hours: number;
}

export function causeKey(code: string): MessageKey {
  return (CAUSES as readonly string[]).includes(code) ? (`insight.cause.${code}` as MessageKey) : 'insight.cause.other';
}

export function periodKey(p: EnergyPeriod): MessageKey {
  return `insight.energy.${p}`;
}

/** One decimal under 10, none above: an estimate should not look exact. */
export function rounded(v: number): string {
  return v < 10 ? v.toFixed(1) : Math.round(v).toString();
}

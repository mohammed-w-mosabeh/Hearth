// Crashes and out-of-memory kills (ROADMAP 6.3): GET /api/v1/crashes.

import type { MessageKey } from './i18n';

export interface Owner {
  kind: 'unit' | 'container' | 'app' | string;
  name: string;
}

export interface CrashEvent {
  t: number;
  kind: 'crash' | 'oom' | string;
  name: string;
  exe?: string;
  signal?: string;
  owner?: Owner;
}

export interface CrashDay {
  day: string;
  crash: number;
  oom: number;
}

export interface CrashReport {
  events: CrashEvent[];
  days: CrashDay[];
  limited?: boolean;
  coredump: boolean;
  truncated?: boolean;
}

export const CRASH_DAYS = [7, 30, 90] as const;

const OWNERS: Record<string, MessageKey> = {
  unit: 'crashes.owner.unit',
  container: 'crashes.owner.container',
  app: 'crashes.owner.app',
};

export function ownerKey(o: Owner): MessageKey | null {
  return OWNERS[o.kind] ?? null;
}

/** The tallest day, for scaling the bars; at least 1. */
export function peakDay(days: readonly CrashDay[]): number {
  return Math.max(1, ...days.map((d) => d.crash + d.oom));
}

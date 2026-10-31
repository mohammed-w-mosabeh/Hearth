// Disk health (ROADMAP 5.3): GET /api/v1/disks/health, internal/smart.

import type { MessageKey } from './i18n';

export type HealthStatus = 'ok' | 'unavailable' | 'not-member' | 'not-installed';

export interface SmartTest {
  kind: string;
  result: string;
  passed: boolean;
  hours?: number;
}

export interface SmartDisk {
  device: string;
  model?: string;
  protocol?: string;
  capacity?: number;
  supported: boolean;
  asleep?: boolean;
  failing: boolean;
  temp_c?: number;
  power_on_hours?: number;
  reallocated?: number;
  pending?: number;
  media_errors?: number;
  life_pct?: number;
  testing: boolean;
  remaining_pct?: number;
  last_test?: SmartTest;
  error?: string;
  at?: number;
}

export interface Health {
  status: HealthStatus;
  disks: SmartDisk[];
}

export type Verdict = 'failing' | 'ok' | 'asleep' | 'unsupported' | 'unknown';

/**
 * A disk's one-word state. Failing comes first, also from figures older
 * than this read: a disk that said so does not stop being at risk by
 * going to sleep.
 */
export function verdict(d: SmartDisk): Verdict {
  if (d.failing) return 'failing';
  if (!d.at) return d.asleep ? 'asleep' : 'unknown';
  return d.supported ? 'ok' : 'unsupported';
}

export const verdictKeys: Record<Verdict, MessageKey> = {
  failing: 'disks.health.failing',
  ok: 'disks.health.ok',
  asleep: 'disks.health.asleep',
  unsupported: 'disks.health.unsupported',
  unknown: 'disks.health.unknown',
};

export const statusKeys: Record<Exclude<HealthStatus, 'ok'>, MessageKey> = {
  unavailable: 'disks.health.unavailable',
  'not-member': 'disks.health.notMember',
  'not-installed': 'disks.health.notInstalled',
};

/** Reallocated, pending or media errors: figures worth a warning colour. */
export function worrying(n: number | undefined): boolean {
  return (n ?? 0) > 0;
}

/** Whole days from power-on hours. */
export function days(hours: number): number {
  return Math.floor(hours / 24);
}

/** How often to read again: only while a test runs (its percentage moves). */
export function pollMs(disks: SmartDisk[]): number | null {
  return disks.some((d) => d.testing) ? 30_000 : null;
}

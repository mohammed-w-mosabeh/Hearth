// Boot tab: types of internal/bootinfo and helpers to show them.

import type { MessageKey } from './i18n';

export const PHASES = ['firmware', 'loader', 'kernel', 'initrd', 'userspace'] as const;

export interface Phase {
  name: string;
  ms: number;
}

export interface Unit {
  name: string;
  ms: number;
}

export interface Link {
  name: string;
  at_ms: number;
  ms: number;
}

export interface BootReport {
  finished: boolean;
  total_ms: number;
  phases: Phase[];
  target?: string;
  target_ms?: number;
  blame: Unit[];
  chain: Link[];
  error?: string;
}

export function phaseKey(name: string): MessageKey {
  return (PHASES as readonly string[]).includes(name) ? (`boot.phase.${name}` as MessageKey) : 'boot.phase.userspace';
}

/** "189 ms", "26.1 s", "2 min 7 s". */
export function spanText(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  const s = Math.round(ms / 1000);
  return `${Math.floor(s / 60)} min ${s % 60} s`;
}

/** Whether a chain step took a large share of the wait for the target. */
export function slowLink(l: Link, chain: readonly Link[]): boolean {
  const most = Math.max(...chain.map((c) => c.ms));
  return l.ms > 0 && l.ms === most;
}

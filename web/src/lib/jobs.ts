// Scheduler (internal/scheduler): job types, the visual schedule builder
// (to and from systemd OnCalendar) and display helpers.

export type ScheduleKind = 'calendar' | 'boot' | 'interval';
export type Scope = 'user' | 'system';

export interface Schedule {
  kind: ScheduleKind;
  value: string;
}

export interface Job {
  id: string;
  name: string;
  script: string;
  schedule: Schedule;
  timeout: number;
  notify: boolean;
}

export interface Listed extends Job {
  scope: Scope;
  paused: boolean;
  running: boolean;
  failed: boolean;
  next?: number;
  last?: number;
  result?: string;
  exit?: number;
}

export interface Check {
  valid: boolean;
  error?: string;
  normalized?: string;
  next?: number[];
}

export interface Run {
  start: number;
  end?: number;
  result: 'success' | 'failed' | 'running';
  exit?: number;
  output: string[];
  more?: boolean;
}

export const DAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const;
export type Day = (typeof DAYS)[number];

/** The visual builder's state. */
export type Builder =
  | { freq: 'minutes'; every: number }
  | { freq: 'hourly'; minute: number }
  | { freq: 'daily'; time: string }
  | { freq: 'weekly'; days: Day[]; time: string }
  | { freq: 'monthly'; day: number; time: string }
  | { freq: 'custom'; expr: string };

const pad = (n: number) => String(n).padStart(2, '0');
const timeRe = /^([01]\d|2[0-3]):([0-5]\d)$/;

/** The OnCalendar expression of a builder state, or null when incomplete. */
export function toCalendar(b: Builder): string | null {
  switch (b.freq) {
    case 'minutes':
      return Number.isInteger(b.every) && b.every >= 1 && b.every <= 59 ? `*:0/${b.every}` : null;
    case 'hourly':
      return Number.isInteger(b.minute) && b.minute >= 0 && b.minute <= 59 ? `*-*-* *:${pad(b.minute)}:00` : null;
    case 'daily':
      return timeRe.test(b.time) ? `*-*-* ${b.time}:00` : null;
    case 'weekly': {
      if (!timeRe.test(b.time) || b.days.length === 0) return null;
      const days = DAYS.filter((d) => b.days.includes(d)).join(',');
      return `${days} *-*-* ${b.time}:00`;
    }
    case 'monthly':
      return timeRe.test(b.time) && Number.isInteger(b.day) && b.day >= 1 && b.day <= 28 ? `*-*-${pad(b.day)} ${b.time}:00` : null;
    case 'custom':
      return b.expr.trim() || null;
  }
}

/** The builder state that writes expr, or a custom one. */
export function fromCalendar(expr: string): Builder {
  let m: RegExpExecArray | null;
  if ((m = /^\*:0\/(\d{1,2})$/.exec(expr))) return { freq: 'minutes', every: Number(m[1]) };
  if ((m = /^\*-\*-\* \*:(\d\d):00$/.exec(expr))) return { freq: 'hourly', minute: Number(m[1]) };
  if ((m = /^\*-\*-\* (\d\d:\d\d):00$/.exec(expr))) return { freq: 'daily', time: m[1] ?? '' };
  if ((m = /^([A-Z][a-z]{2}(?:,[A-Z][a-z]{2})*) \*-\*-\* (\d\d:\d\d):00$/.exec(expr))) {
    const days = (m[1] ?? '').split(',');
    if (days.every((d) => (DAYS as readonly string[]).includes(d))) return { freq: 'weekly', days: days as Day[], time: m[2] ?? '' };
  }
  if ((m = /^\*-\*-(\d\d) (\d\d:\d\d):00$/.exec(expr))) {
    const day = Number(m[1]);
    if (day >= 1 && day <= 28) return { freq: 'monthly', day, time: m[2] ?? '' };
  }
  return { freq: 'custom', expr };
}

/** A schedule in words, for the list. */
export function describe(s: Schedule): string {
  if (s.kind === 'boot') return `${s.value} after boot`;
  if (s.kind === 'interval') return `every ${s.value}`;
  const b = fromCalendar(s.value);
  switch (b.freq) {
    case 'minutes':
      return b.every === 1 ? 'every minute' : `every ${b.every} minutes`;
    case 'hourly':
      return `hourly at :${pad(b.minute)}`;
    case 'daily':
      return `daily at ${b.time}`;
    case 'weekly':
      return b.days.length === 7 ? `daily at ${b.time}` : `${b.days.join(', ')} at ${b.time}`;
    case 'monthly':
      return `monthly on day ${b.day} at ${b.time}`;
    case 'custom':
      return s.value;
  }
}

export const NEW_SCRIPT = '#!/bin/sh\n# Runs with no terminal; output goes to the job history.\n\n';

/** An empty job for the form. */
export function blank(): Job {
  return { id: '', name: '', script: NEW_SCRIPT, schedule: { kind: 'calendar', value: '*-*-* 03:00:00' }, timeout: 3600, notify: true };
}

/** Client-side form check mirroring the server's; null when valid. */
export function jobError(j: Job): string | null {
  const name = j.name;
  if (!name.trim() || name !== name.trim()) return 'name';
  const control = [...name].some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f);
  if ([...name].length > 80 || control || name.endsWith('\\')) return 'name';
  if (!j.script.startsWith('#!/')) return 'script';
  if (new TextEncoder().encode(j.script).length > 64 * 1024) return 'scriptSize';
  if (!Number.isInteger(j.timeout) || j.timeout < 1 || j.timeout > 7 * 24 * 3600) return 'timeout';
  if (!j.schedule.value.trim()) return 'schedule';
  return null;
}

/** Jobs: failed first, then running, then by next run. */
export function order(list: Listed[]): Listed[] {
  const rank = (j: Listed) => (j.failed ? 0 : j.running ? 1 : j.paused ? 3 : 2);
  return [...list].sort((a, b) => rank(a) - rank(b) || (a.next ?? Infinity) - (b.next ?? Infinity) || a.name.localeCompare(b.name));
}

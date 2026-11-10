// Home's health ring and Needs attention queue (ROADMAP 8.6). Both come from
// the same items, read from existing endpoints; the score is 100 minus fixed
// points per item, each source capped, with nothing else weighed in. The
// table is documented in docs/design/ember.md.

import type { AlertEvent } from './alerts';
import type { Forecasts } from './disks';
import type { LynisView } from './lynis';
import type { Report } from './security';
import type { AppHealth } from './stacks';
import type { Unit } from './services';
import type { Health } from './smart';

export type Source = 'unit' | 'alert.critical' | 'alert.warning' | 'alert.info' | 'updates' | 'listener' | 'forecast' | 'restart' | 'smart' | 'lynis' | 'app' | 'backup' | 'appUpdate' | 'approval';

/** Points per item and the most one source can take. */
export const RULES: Record<Source, { each: number; cap: number }> = {
  unit: { each: 10, cap: 30 },
  'alert.critical': { each: 15, cap: 30 },
  'alert.warning': { each: 5, cap: 15 },
  'alert.info': { each: 0, cap: 0 },
  updates: { each: 15, cap: 15 },
  listener: { each: 5, cap: 15 },
  forecast: { each: 15, cap: 30 },
  restart: { each: 5, cap: 5 },
  smart: { each: 25, cap: 50 },
  lynis: { each: 10, cap: 10 },
  app: { each: 10, cap: 30 },
  backup: { each: 15, cap: 30 },
  appUpdate: { each: 5, cap: 10 },
  approval: { each: 0, cap: 0 },
};

/** A stack step an automatic mode switch left for the user (ROADMAP 10.7),
 *  from GET /modes. */
export interface PendingApproval {
  id: number;
  mode: string;
  name: string;
  reason: string;
}

/** Which endpoint feeds which sources: one that failed is "not checked". */
export type Feed = 'units' | 'alerts' | 'security' | 'forecasts' | 'smart' | 'lynis' | 'apps' | 'modes';

export const FEED_SOURCES: Record<Feed, Source[]> = {
  units: ['unit'],
  alerts: ['alert.critical', 'alert.warning', 'alert.info'],
  security: ['updates', 'listener', 'restart'],
  forecasts: ['forecast'],
  smart: ['smart'],
  lynis: ['lynis'],
  apps: ['app', 'backup', 'appUpdate'],
  modes: ['approval'],
};

export type Action = { kind: 'unit'; unit: Unit } | { kind: 'open'; route: string; q?: string };

export interface Item {
  /** Stable across reads, so a snooze keeps applying. */
  id: string;
  source: Source;
  /** System-derived text, shown as text only. */
  subject: string;
  action: Action;
}

/** undefined: still loading; null: could not be read. */
export type Inputs = { [F in Feed]?: FeedData[F] | null | undefined };

export interface FeedData {
  units: Unit[];
  alerts: AlertEvent[];
  security: Report;
  forecasts: Forecasts;
  smart: Health;
  lynis: LynisView;
  apps: AppHealth[];
  modes: PendingApproval[];
}

/** A disk forecast counts when the disk fills within this many days. */
export const FORECAST_DAYS = 7;

export function collect(inp: Inputs): Item[] {
  const out: Item[] = [];
  for (const u of inp.units ?? []) {
    if (u.active === 'failed')
      out.push({
        id: `unit:${u.scope}/${u.name}`,
        source: 'unit',
        subject: u.name,
        action: { kind: 'unit', unit: u },
      });
  }
  for (const e of inp.alerts ?? []) {
    if (e.ended_at) continue;
    const subject = e.instance ? `${e.rule_name} (${e.instance})` : e.rule_name;
    out.push({
      id: `alert:${e.id}`,
      source: `alert.${e.severity}`,
      subject,
      action: { kind: 'open', route: 'alerts' },
    });
  }
  const checks = inp.security?.checks ?? [];
  for (const c of checks) {
    for (const f of c.findings) {
      if (f.code === 'updates.security') {
        out.push({
          id: 'updates.security',
          source: 'updates',
          subject: f.subject ?? '',
          action: { kind: 'open', route: 'packages' },
        });
      } else if (f.code === 'listener.exposed' && f.severity !== 'info') {
        // Behind an active firewall the Security page calls it info, and
        // so does this: only the firewall's rules let anyone reach it.
        const s = f.subject ?? '';
        out.push({
          id: `listener:${s}`,
          source: 'listener',
          subject: s,
          action: { kind: 'open', route: 'security' },
        });
      }
    }
  }
  const restart = checks.find((c) => c.id === 'restart');
  const codes = new Set(restart?.findings.map((f) => f.code));
  if (codes.has('restart.reboot') || codes.has('restart.kernel') || codes.has('restart.services')) {
    const subject = codes.has('restart.reboot') || codes.has('restart.kernel') ? 'reboot' : 'services';
    out.push({
      id: 'restart',
      source: 'restart',
      subject,
      action: { kind: 'open', route: 'security' },
    });
  }
  for (const d of inp.forecasts?.disks ?? []) {
    if (d.days < FORECAST_DAYS)
      out.push({
        id: `forecast:${d.mount}`,
        source: 'forecast',
        subject: d.mount,
        action: { kind: 'open', route: 'space' },
      });
  }
  for (const d of inp.smart?.disks ?? []) {
    if (d.failing)
      out.push({
        id: `smart:${d.device}`,
        source: 'smart',
        subject: d.model ? `${d.device} (${d.model})` : d.device,
        action: { kind: 'open', route: 'disks' },
      });
  }
  const [now, before] = inp.lynis?.history ?? [];
  if (now && before && now.index < before.index) {
    out.push({
      id: `lynis:${now.time}`,
      source: 'lynis',
      subject: `${before.index} → ${now.index}`,
      action: { kind: 'open', route: 'security' },
    });
  }
  for (const a of inp.apps ?? []) {
    const open: Action = { kind: 'open', route: 'stacks' };
    if (a.unhealthy.length > 0) out.push({ id: `app:${a.name}`, source: 'app', subject: `${a.name} (${a.unhealthy.join(', ')})`, action: open });
    // Keyed by the backup's time: a new failure is a new row, unsnoozed.
    if (a.backup && !a.backup.ok) out.push({ id: `backup:${a.name}:${a.backup.at}`, source: 'backup', subject: a.name, action: open });
    if (a.updates.length > 0) out.push({ id: `update:${a.name}`, source: 'appUpdate', subject: `${a.name} (${a.updates.join(', ')})`, action: open });
  }
  for (const ap of inp.modes ?? []) {
    out.push({ id: `approval:${ap.id}`, source: 'approval', subject: ap.name || ap.mode, action: { kind: 'open', route: 'modes' } });
  }
  return out;
}

export interface Deduction {
  source: Source;
  count: number;
  points: number;
}

/** The score and every deduction that applies, in RULES order. */
export function score(items: Item[]): {
  score: number;
  deductions: Deduction[];
} {
  const counts = new Map<Source, number>();
  for (const it of items) counts.set(it.source, (counts.get(it.source) ?? 0) + 1);
  const deductions: Deduction[] = [];
  for (const source of Object.keys(RULES) as Source[]) {
    const count = counts.get(source) ?? 0;
    if (count === 0) continue;
    const r = RULES[source];
    deductions.push({ source, count, points: Math.min(r.cap, r.each * count) });
  }
  const total = deductions.reduce((s, d) => s + d.points, 0);
  return { score: Math.max(0, 100 - total), deductions };
}

/** Sources that could not be read, so the score does not cover them. */
export function unchecked(inp: Inputs): Feed[] {
  return (Object.keys(FEED_SOURCES) as Feed[]).filter((f) => inp[f] === null);
}

/** critical first, then warning, then the rest; stable otherwise. */
export function severity(s: Source): 'failed' | 'warning' | 'degraded' {
  if (s === 'unit' || s === 'alert.critical' || s === 'smart' || s === 'updates' || s === 'forecast' || s === 'backup') return 'failed';
  if (s === 'alert.info' || s === 'restart' || s === 'appUpdate') return 'degraded';
  return 'warning';
}

const rank = { failed: 0, warning: 1, degraded: 2 };

// Snoozing (24 h, per viewer, stored in the home.snoozed setting).

export const SNOOZE_MS = 24 * 60 * 60 * 1000;
/** As internal/api/settings.go allows. */
export const MAX_SNOOZED = 200;

export type Snoozed = Record<string, number>;

/** The stored value, keeping only well-formed, unexpired entries. */
export function readSnoozed(v: unknown, now: number): Snoozed {
  const until = (v as { until?: unknown } | null)?.until;
  const out: Snoozed = {};
  if (!until || typeof until !== 'object' || Array.isArray(until)) return out;
  for (const [id, t] of Object.entries(until as Record<string, unknown>)) {
    if (typeof t === 'number' && Number.isInteger(t) && t > now && id.length > 0 && id.length <= 300) out[id] = t;
  }
  return out;
}

/** s with id snoozed until now + 24 h, the soonest to end dropped if full. */
export function snooze(s: Snoozed, id: string, now: number): Snoozed {
  const all = { ...readSnoozed({ until: s }, now), [id]: now + SNOOZE_MS };
  const kept = Object.entries(all)
    .sort((a, b) => b[1] - a[1])
    .slice(0, MAX_SNOOZED);
  return Object.fromEntries(kept);
}

/** The queue: what is not snoozed, the most serious first. */
export function queue(items: Item[], s: Snoozed, now: number): Item[] {
  return items
    .filter((it) => !((s[it.id] ?? 0) > now))
    .map((it, i) => ({ it, i }))
    .sort((a, b) => rank[severity(a.it.source)] - rank[severity(b.it.source)] || a.i - b.i)
    .map((x) => x.it);
}

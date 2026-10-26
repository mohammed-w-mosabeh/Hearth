// Alerts page: types and text (internal/alerts, /api/v1/alerts/*).

import { t, type MessageKey } from './i18n';

export type Severity = 'info' | 'warning' | 'critical';

export interface Rule {
  id: number;
  name: string;
  metric: string;
  target: string;
  op: '>' | '<';
  threshold: number;
  for_s: number;
  severity: Severity;
  enabled: boolean;
  quiet_from: string;
  quiet_to: string;
}

export interface AlertEvent {
  id: number;
  rule_id: number;
  rule_name: string;
  metric: string;
  instance: string;
  severity: Severity;
  op: '>' | '<';
  threshold: number;
  value: number;
  peak: number;
  started_at: number;
  ended_at?: number;
  notified: boolean;
}

export interface Metric {
  id: string;
  label: string;
  unit: string;
  instance?: string;
  hysteresis: number;
  min: number;
  max: number;
  instances: string[];
}

export type RuleDraft = Omit<Rule, 'id'>;

const metricKeys: Record<string, MessageKey> = {
  'cpu.usage': 'metric.cpu.usage',
  'mem.used_pct': 'metric.mem.used_pct',
  'swap.used_pct': 'metric.swap.used_pct',
  'disk.used_pct': 'metric.disk.used_pct',
  'cpu.temp': 'metric.cpu.temp',
  'gpu.temp': 'metric.gpu.temp',
  'battery.discharging': 'metric.battery.discharging',
  'unit.failed': 'metric.unit.failed',
};
const instanceKeys: Record<string, MessageKey> = {
  mount: 'instance.mount',
  gpu: 'instance.gpu',
  battery: 'instance.battery',
  unit: 'instance.unit',
};

/** The metric's name in the UI language (the server's English label otherwise). */
export function metricLabel(m: Metric | undefined, id: string): string {
  const k = metricKeys[id];
  return k ? t(k) : (m?.label ?? id);
}

/** What one instance of the metric is called ("mount", "battery"...). */
export function instanceLabel(m: Metric | undefined): string {
  const k = m?.instance ? instanceKeys[m.instance] : undefined;
  return k ? t(k) : (m?.instance ?? '');
}

/** A rule's editable fields. */
export function draftOf(r: Rule): RuleDraft {
  return {
    name: r.name,
    metric: r.metric,
    target: r.target,
    op: r.op,
    threshold: r.threshold,
    for_s: r.for_s,
    severity: r.severity,
    enabled: r.enabled,
    quiet_from: r.quiet_from,
    quiet_to: r.quiet_to,
  };
}

/** A new rule's starting values. */
export function blankRule(m: Metric | undefined): RuleDraft {
  return {
    name: '',
    metric: m?.id ?? 'cpu.usage',
    target: '',
    op: '>',
    threshold: m && m.unit === '' ? 0 : 90,
    for_s: 60,
    severity: 'warning',
    enabled: true,
    quiet_from: '',
    quiet_to: '',
  };
}

/** "for 45 s", "for 3 min", "for 1 h 30 min"; 0 is "at once". */
export function forText(s: number): string {
  if (s <= 0) return t('alerts.for.once');
  if (s < 60) return t('alerts.for.s', { s });
  const m = Math.floor(s / 60);
  const rest = s % 60;
  if (m < 60) return rest ? t('alerts.for.minS', { m, s: rest }) : t('alerts.for.min', { m });
  const h = Math.floor(m / 60);
  return m % 60 ? t('alerts.for.hMin', { h, m: m % 60 }) : t('alerts.for.h', { h });
}

/** A value with the metric's unit. */
export function valueText(v: number, m: Metric | undefined): string {
  if (!m || m.unit === '') return String(Math.round(v));
  return m.unit === '%' ? `${Math.round(v)}%` : `${Math.round(v)} ${m.unit}`;
}

/** "Disk space used (any mount) > 90% for 1 min". */
export function conditionText(r: Pick<Rule, 'metric' | 'target' | 'op' | 'threshold' | 'for_s'>, m: Metric | undefined): string {
  if (r.metric === 'unit.failed') return r.target ? t('alerts.cond.unit', { unit: r.target }) : t('alerts.cond.anyUnit');
  const where = m?.instance ? ` (${r.target || t('alerts.cond.any', { what: instanceLabel(m) })})` : '';
  const when = r.for_s > 0 ? ` ${forText(r.for_s)}` : '';
  return `${metricLabel(m, r.metric)}${where} ${r.op} ${valueText(r.threshold, m)}${when}`;
}

/** Client-side check mirroring the server's; null when valid. */
export function ruleError(r: RuleDraft, m: Metric | undefined): string | null {
  if (!m) return t('alerts.err.metric');
  if (r.name.trim() === '' || r.name.length > 80) return t('alerts.err.name');
  if (!Number.isFinite(r.threshold) || r.threshold < m.min || r.threshold > m.max) return t('alerts.err.threshold', { min: m.min, max: m.max });
  if (!Number.isInteger(r.for_s) || r.for_s < 0 || r.for_s > 86400) return t('alerts.err.for');
  if ((r.quiet_from === '') !== (r.quiet_to === '')) return t('alerts.err.quietBoth');
  if (r.quiet_from !== '' && r.quiet_from === r.quiet_to) return t('alerts.err.quietSame');
  return null;
}

/** How long an incident lasted, or has lasted until now. */
export function lasted(e: AlertEvent, now: number = Date.now()): string {
  const s = Math.max(0, Math.round(((e.ended_at ?? now) - e.started_at) / 1000));
  if (s < 60) return t('alerts.lasted.s', { s });
  const m = Math.round(s / 60);
  return m < 60 ? t('alerts.lasted.min', { m }) : t('alerts.lasted.hMin', { h: Math.floor(m / 60), m: m % 60 });
}

/** Applies a live open/close message to the history list (newest first). */
export function applyEvent(list: AlertEvent[], e: AlertEvent): AlertEvent[] {
  return list.some((x) => x.id === e.id) ? list.map((x) => (x.id === e.id ? e : x)) : [e, ...list];
}

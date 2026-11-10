// Automations page (ROADMAP 12.9): types of internal/automations and its
// spec, and helpers to show them. Everything the server sends is shown as
// text (S6); words come from i18n keys, the server's own sentences only
// where it has nothing closed to say (an error's detail).

import type { MessageKey } from './i18n';

export interface Trigger {
  kind: string;
  calendar?: string;
  metric?: string;
  op?: string;
  threshold?: number;
  for_s?: number;
  target?: string;
  scope?: string;
  event?: string;
  state?: string;
  uuid?: string;
  count?: number;
  minutes?: number;
  game?: boolean;
  app?: string;
  folder?: string;
  days?: number;
  depth?: number;
  pattern?: string;
  severity?: string;
}

export interface Condition {
  kind: string;
  from?: string;
  to?: string;
  days?: string[];
  on?: string;
  op?: string;
  percent?: number;
  uuid?: string;
  mode?: string;
  is?: boolean;
  target?: string;
  scope?: string;
  state?: string;
}

export interface Conditions {
  match: 'all' | 'any';
  conditions: Condition[];
}

export interface Step {
  step: 'action' | 'notify' | 'wait' | 'check';
  action?: string;
  target?: string;
  params?: Record<string, string>;
  continue?: boolean;
  label?: string;
  channels?: string[];
  text?: string;
  severity?: string;
  seconds?: number;
  what?: string;
  expect?: string;
  then?: Step[];
  else?: Step[];
}

export interface Options {
  cooldown_s: number;
  max_per_hour: number;
  quiet_from?: string;
  quiet_to?: string;
  run_if_missed?: boolean;
}

export interface Spec {
  trigger: Trigger;
  if?: Conditions;
  then: Step[];
  options: Options;
}

export interface RunSummary {
  id: number;
  state: string;
  started: number;
  by: string;
  reason?: string;
}

/** An automation as GET /automations lists it. */
export interface Automation {
  id: number;
  name: string;
  enabled: boolean;
  origin: string;
  spec?: Spec;
  /** The stored spec no longer passes the validator: it never runs. */
  invalid?: string;
  created: number;
  updated: number;
  last?: RunSummary;
  next?: number;
  waiting?: number;
}

export interface StepResult {
  step: string;
  action?: string;
  target?: string;
  label?: string;
  result: string;
  detail?: string;
  duration_ms: number;
}

export interface RunRecord {
  trigger: { kind: string; instance?: string; value?: number; detail?: string; time: string };
  reason?: string;
  conditions?: boolean;
  values?: string[];
  steps: StepResult[];
  by: string;
}

export interface Run {
  id: number;
  automation_id: number;
  started: number;
  finished: number;
  state: string;
  record: RunRecord;
}

/** Steps an automation left for the user (GET /automations/approvals). */
export interface Approval {
  id: number;
  automation_id: number;
  name: string;
  run_id: number;
  created: number;
  expires: number;
  steps: StepResult[];
}

/** What a row of the list is: a mode's trigger and an alert rule are
 *  changed on their own pages; an invalid one can only be deleted. */
export type RowKind = 'mode' | 'alert' | 'invalid' | 'plain';

export function rowKind(a: Automation): RowKind {
  if (a.origin.startsWith('mode:')) return 'mode';
  if (!a.spec) return 'invalid';
  if (a.spec.trigger.kind === 'alert') return 'alert';
  return 'plain';
}

export type Status = 'paused' | 'waiting' | 'on' | 'off' | 'invalid';

export function status(a: Automation, paused: boolean): Status {
  if (!a.spec) return 'invalid';
  if (!a.enabled) return 'off';
  if ((a.waiting ?? 0) > 0) return 'waiting';
  return paused ? 'paused' : 'on';
}

export const TRIGGERS = [
  'schedule',
  'login',
  'metric',
  'alert',
  'container',
  'unit_failed',
  'new_listener',
  'usb_device',
  'security_updates',
  'image_update',
  'integrity_changed',
  'ssh_failed',
  'power',
  'wifi',
  'vpn',
  'internet',
  'idle',
  'resume',
  'display',
  'app',
  'files',
] as const;
export type TriggerKind = (typeof TRIGGERS)[number];

export function isTrigger(k: string): k is TriggerKind {
  return (TRIGGERS as readonly string[]).includes(k);
}

export const METRICS = ['cpu.usage', 'mem.used_pct', 'swap.used_pct', 'disk.used_pct', 'cpu.temp', 'gpu.temp', 'battery.discharging'] as const;

export function isMetric(m: string): m is (typeof METRICS)[number] {
  return (METRICS as readonly string[]).includes(m);
}

/** A trigger in words: a key and its values. Names (units, containers,
 *  connections) are passed in as values, never put in markup. */
export interface Names {
  /** A connection's name by UUID. */
  connection?: (uuid: string) => string;
  /** A metric's name in the UI language (alerts.metricLabel). */
  metric?: (id: string) => string;
}

export function triggerWords(tr: Trigger, names: Names = {}): { key: MessageKey; values: Record<string, string | number> } {
  const connection = names.connection ?? ((u: string) => u);
  const metric = names.metric ?? ((m: string) => m);
  const target = tr.target ?? '';
  switch (tr.kind) {
    case 'schedule':
      return { key: 'auto.trig.schedule', values: { s: tr.calendar ?? '' } };
    case 'metric':
    case 'alert': {
      const m = tr.metric ?? '';
      return {
        key: tr.kind === 'alert' ? 'auto.trig.alert' : 'auto.trig.metric',
        values: { metric: metric(m), op: tr.op ?? '', n: tr.threshold ?? 0, target: target ? ` (${target})` : '', s: tr.for_s ?? 0 },
      };
    }
    case 'container':
      return { key: `auto.trig.container.${tr.event === 'unhealthy' ? 'unhealthy' : tr.event === 'oom' ? 'oom' : 'died'}`, values: { s: target || '*' } };
    case 'unit_failed':
      return { key: 'auto.trig.unit_failed', values: { s: target || '*' } };
    case 'wifi':
    case 'vpn':
      return { key: `auto.trig.${tr.kind}.${tr.state === 'down' ? 'down' : 'up'}`, values: { s: connection(tr.uuid ?? '') } };
    case 'internet':
      return { key: tr.state === 'back' ? 'auto.trig.internet.back' : 'auto.trig.internet.down', values: {} };
    case 'power':
      return { key: tr.event === 'plugged' ? 'auto.trig.power.plugged' : 'auto.trig.power.unplugged', values: {} };
    case 'display':
      return { key: tr.event === 'disconnected' ? 'auto.trig.display.disconnected' : 'auto.trig.display.connected', values: {} };
    case 'idle':
      return { key: 'auto.trig.idle', values: { n: tr.minutes ?? 0 } };
    case 'ssh_failed':
      return { key: 'auto.trig.ssh_failed', values: { n: tr.count ?? 0 } };
    case 'app':
      return {
        key: tr.game ? (tr.state === 'stopped' ? 'auto.trig.game.stopped' : 'auto.trig.game.started') : tr.state === 'stopped' ? 'auto.trig.app.stopped' : 'auto.trig.app.started',
        values: { s: tr.app ?? '' },
      };
    case 'files':
      return { key: tr.event === 'older_than' ? 'auto.trig.files.old' : 'auto.trig.files.new', values: { s: tr.folder ?? '', n: tr.days ?? 0 } };
    case 'image_update':
    case 'integrity_changed':
      return { key: `auto.trig.${tr.kind}`, values: { s: target || '*' } };
    case 'login':
    case 'new_listener':
    case 'usb_device':
    case 'security_updates':
    case 'resume':
      return { key: `auto.trig.${tr.kind}`, values: {} };
  }
  return { key: 'auto.trig.unknown', values: { s: tr.kind } };
}

export const RUN_STATES = ['running', 'done', 'failed', 'skipped', 'waiting', 'limited'] as const;

export function runStateKey(s: string): MessageKey {
  return (RUN_STATES as readonly string[]).includes(s) ? (`auto.run.${s}` as MessageKey) : 'auto.run.other';
}

// The engine records why a run did not go in its own words (a closed set);
// they are worded here, and anything else is shown as it is.
const REASONS: Record<string, MessageKey> = {
  'all automations are paused': 'auto.why.paused',
  'the global limit was reached': 'auto.why.limit',
  'quiet hours': 'auto.why.quiet',
  'a run is still going': 'auto.why.running',
  cooldown: 'auto.why.cooldown',
  'the hourly maximum was reached': 'auto.why.hourly',
  'it waited too long': 'auto.why.expired',
  'the automation was deleted': 'auto.why.deleted',
  'the automation was turned off': 'auto.why.off',
  'the automation was edited': 'auto.why.edited',
};

/** A run's reason as a key, or null to show the server's words. */
export function reasonKey(reason: string): MessageKey | null {
  return REASONS[reason] ?? null;
}

export const STEP_RESULTS = ['ok', 'failed', 'waiting', 'skipped'] as const;

export function stepResultKey(r: string): MessageKey {
  return (STEP_RESULTS as readonly string[]).includes(r) ? (`auto.step.${r}` as MessageKey) : 'auto.step.other';
}

/** A step in words: what it does, without its result. */
/** What stepWords reads: a spec's step or a run's step result. */
export interface StepLike {
  step: string;
  action?: string | undefined;
  target?: string | undefined;
  label?: string | undefined;
  seconds?: number | undefined;
}

export function stepWords(s: StepLike): { key: MessageKey; values: Record<string, string | number> } {
  if (s.label) return { key: 'auto.stepLabel', values: { s: s.label } };
  switch (s.step) {
    case 'action':
      return { key: 'auto.stepAction', values: { action: s.action ?? '', target: s.target ?? '' } };
    case 'notify':
      return { key: 'auto.stepNotify', values: {} };
    case 'wait':
      return { key: 'auto.stepWait', values: { n: s.seconds ?? 0 } };
    case 'check':
      return { key: 'auto.stepCheck', values: { target: s.target ?? '' } };
  }
  return { key: 'auto.stepAction', values: { action: s.step, target: '' } };
}

/** Approvals from GET /automations/approvals: the automations' only; the
 *  modes' are listed by the Modes page and Home already. */
export interface ApprovalList {
  automations: Approval[];
}

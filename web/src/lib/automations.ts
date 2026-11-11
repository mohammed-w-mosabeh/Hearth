// Automations page (ROADMAP 12.9): types of internal/automations and its
// spec, and helpers to show them. Everything the server sends is shown as
// text (S6); words come from i18n keys, the server's own sentences only
// where it has nothing closed to say (an error's detail).

import type { MessageKey } from './i18n';

export interface Trigger {
  kind: string;
  calendar?: string | undefined;
  metric?: string | undefined;
  op?: string | undefined;
  threshold?: number | undefined;
  for_s?: number | undefined;
  target?: string | undefined;
  scope?: string | undefined;
  event?: string | undefined;
  state?: string | undefined;
  uuid?: string | undefined;
  count?: number | undefined;
  minutes?: number | undefined;
  game?: boolean | undefined;
  app?: string | undefined;
  folder?: string | undefined;
  days?: number | undefined;
  depth?: number | undefined;
  pattern?: string | undefined;
  severity?: string | undefined;
}

export interface Condition {
  kind: string;
  from?: string | undefined;
  to?: string | undefined;
  days?: string[] | undefined;
  on?: string | undefined;
  op?: string | undefined;
  percent?: number | undefined;
  uuid?: string | undefined;
  mode?: string | undefined;
  is?: boolean | undefined;
  target?: string | undefined;
  scope?: string | undefined;
  state?: string | undefined;
}

export interface Conditions {
  match: 'all' | 'any';
  conditions: Condition[];
}

export interface Step {
  step: 'action' | 'notify' | 'wait' | 'check';
  action?: string | undefined;
  target?: string | undefined;
  params?: Record<string, string> | undefined;
  continue?: boolean | undefined;
  label?: string | undefined;
  channels?: string[] | undefined;
  text?: string | undefined;
  severity?: string | undefined;
  seconds?: number | undefined;
  what?: string | undefined;
  expect?: string | undefined;
  then?: Step[] | undefined;
  else?: Step[] | undefined;
}

export interface Options {
  cooldown_s: number;
  max_per_hour: number;
  quiet_from?: string | undefined;
  quiet_to?: string | undefined;
  run_if_missed?: boolean | undefined;
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
  /** An alert rule's condition, as the Alerts page words it. */
  alert?: (tr: Trigger) => string;
}

export function triggerWords(tr: Trigger, names: Names = {}): { key: MessageKey; values: Record<string, string | number> } {
  const connection = names.connection ?? ((u: string) => u);
  const metric = names.metric ?? ((m: string) => m);
  const target = tr.target ?? '';
  switch (tr.kind) {
    case 'schedule':
      return { key: 'auto.trig.schedule', values: { s: tr.calendar ?? '' } };
    case 'alert':
      return { key: 'auto.trig.alert', values: { s: names.alert ? names.alert(tr) : metric(tr.metric ?? '') } };
    case 'metric': {
      // The comparison in words: a ">" sign reads backwards in Arabic.
      const values = { metric: metric(tr.metric ?? ''), n: tr.threshold ?? 0, target: target ? ` (${target})` : '', s: tr.for_s ?? 0 };
      const op = tr.op === '<' ? 'below' : 'above';
      return { key: `auto.trig.metric.${op}${(tr.for_s ?? 0) > 0 ? 'For' : ''}`, values };
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

// ---- The editor (ROADMAP 12.9): pickers only; free text only for the
// name, labels and notification text. The spec being edited is the object
// itself, so fields the editor does not show are kept as they were.

/** An action the server offers (GET /automations/actions). */
export interface ActionInfo {
  key: string;
  risk: string;
}

/** Where an action's target is picked from. */
export type Picker = 'fixed' | 'unit' | 'container' | 'stack' | 'connection' | 'app';

export interface CatalogEntry {
  key: string;
  picker: Picker;
  /** The fixed targets, for a fixed picker. */
  options?: readonly string[];
}

/** The actions the editor offers, each with its target picker. One the
 *  server has but is not listed here is not offered for a new step; a
 *  step that holds it is shown and saved as it is. */
export const CATALOG: readonly CatalogEntry[] = [
  { key: 'power/profile', picker: 'fixed', options: ['power-saver', 'balanced', 'performance'] },
  { key: 'session/dnd', picker: 'fixed', options: ['on', 'off'] },
  { key: 'unit/start', picker: 'unit' },
  { key: 'unit/stop', picker: 'unit' },
  { key: 'unit/restart', picker: 'unit' },
  { key: 'container/start', picker: 'container' },
  { key: 'container/stop', picker: 'container' },
  { key: 'container/restart', picker: 'container' },
  { key: 'stack/start', picker: 'stack' },
  { key: 'stack/stop', picker: 'stack' },
  { key: 'stack/restart', picker: 'stack' },
  { key: 'network/connect', picker: 'connection' },
  { key: 'network/disconnect', picker: 'connection' },
  { key: 'app/launch', picker: 'app' },
  { key: 'space/preview', picker: 'fixed', options: ['space'] },
  { key: 'report/generate', picker: 'fixed', options: ['day', 'week', 'month'] },
  { key: 'hearth/backup', picker: 'fixed', options: ['database'] },
  { key: 'debsecan/check', picker: 'fixed', options: ['debsecan'] },
];

/** The catalog's actions the server offers. */
export function offered(server: readonly ActionInfo[]): CatalogEntry[] {
  const keys = new Set(server.map((a) => a.key));
  return CATALOG.filter((c) => keys.has(c.key));
}

export const catalogEntry = (key: string): CatalogEntry | undefined => CATALOG.find((c) => c.key === key);

export function actionKey(key: string): MessageKey {
  return catalogEntry(key) ? (`auto.act.${key.replace('/', '.')}` as MessageKey) : ('auto.act.other' as MessageKey);
}

/** The triggers a new automation may pick: an alert rule is made on the
 *  Alerts page, and a folder has no picker yet. */
export const NEW_TRIGGERS: readonly TriggerKind[] = TRIGGERS.filter((k) => k !== 'alert' && k !== 'files');

/** A trigger of kind with values the validator accepts, or that wait for
 *  one pick (a connection). */
export function blankTrigger(kind: TriggerKind): Trigger {
  switch (kind) {
    case 'schedule':
      return { kind, calendar: '*-*-* 09:00:00' };
    case 'metric':
      return { kind, metric: 'cpu.temp', op: '>', threshold: 90, for_s: 60 };
    case 'container':
      return { kind, event: 'unhealthy' };
    case 'unit_failed':
      return { kind, scope: 'user' };
    case 'ssh_failed':
      return { kind, count: 10 };
    case 'power':
      return { kind, event: 'unplugged' };
    case 'wifi':
    case 'vpn':
      return { kind, uuid: '', state: 'up' };
    case 'internet':
      return { kind, state: 'down' };
    case 'idle':
      return { kind, minutes: 30 };
    case 'display':
      return { kind, event: 'connected' };
    case 'app':
      return { kind, state: 'started', game: true };
    default:
      return { kind };
  }
}

export const CONDITIONS = ['time', 'weekday', 'power', 'battery', 'wifi', 'mode', 'game', 'container', 'unit'] as const;
export type ConditionKind = (typeof CONDITIONS)[number];
export const WEEKDAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const;

export function blankCondition(kind: ConditionKind): Condition {
  switch (kind) {
    case 'time':
      return { kind, from: '22:00', to: '07:00' };
    case 'weekday':
      return { kind, days: ['mon', 'tue', 'wed', 'thu', 'fri'] };
    case 'power':
      return { kind, on: 'ac' };
    case 'battery':
      return { kind, op: 'below', percent: 30 };
    case 'wifi':
      return { kind, uuid: '', is: true };
    case 'mode':
      return { kind, mode: '', is: true };
    case 'game':
      return { kind, is: false };
    case 'container':
      return { kind, target: '', state: 'running' };
    case 'unit':
      return { kind, scope: 'user', target: '', state: 'active' };
  }
}

export const STEP_KINDS = ['action', 'notify', 'wait', 'check'] as const;
export const CHANNELS = ['desktop', 'ntfy', 'mail'] as const;
export const PLACEHOLDERS = ['automation', 'trigger', 'instance', 'value', 'top', 'journal', 'outage', 'sources', 'alert'] as const;
export const CHECKS = { container_health: ['healthy', 'unhealthy'], container_state: ['running', 'stopped'], unit_state: ['active', 'inactive', 'failed'] } as const;
export type CheckWhat = keyof typeof CHECKS;

export function blankStep(kind: Step['step'], first?: CatalogEntry): Step {
  switch (kind) {
    case 'action':
      return { step: 'action', action: first?.key ?? '', target: first?.options?.[0] ?? '' };
    case 'notify':
      return { step: 'notify', channels: ['desktop'], text: '{automation}: {trigger}' };
    case 'wait':
      return { step: 'wait', seconds: 60 };
    case 'check':
      return { step: 'check', what: 'unit_state', target: '', expect: 'active', then: [], else: [] };
  }
}

export const DEFAULT_OPTIONS: Options = { cooldown_s: 300, max_per_hour: 6 };

/** What the editor edits: the automation's fields, and its spec. */
export interface Draft {
  id?: number;
  name: string;
  enabled: boolean;
  spec: Spec;
  /** "template:<id>" for a draft from the gallery (on creation only). */
  origin?: string;
}

export function blankDraft(): Draft {
  return { name: '', enabled: true, spec: { trigger: blankTrigger('schedule'), then: [], options: { ...DEFAULT_OPTIONS } } };
}

/** A deep copy: the editor never changes the list's own object. */
export function draftOf(a: Automation): Draft | null {
  if (!a.spec) return null;
  return { id: a.id, name: a.name, enabled: a.enabled, spec: structuredClone(a.spec) };
}

/** A check moves one step at a time up or down; out of range is a no-op. */
export function moved<T>(list: readonly T[], i: number, by: -1 | 1): T[] {
  const j = i + by;
  if (j < 0 || j >= list.length) return [...list];
  const out = [...list];
  [out[i], out[j]] = [out[j] as T, out[i] as T];
  return out;
}

/** The body create and update take. */
export function body(d: Draft): string {
  return JSON.stringify({ name: d.name, enabled: d.enabled, spec: d.spec, ...(d.id === undefined && d.origin ? { origin: d.origin } : {}) });
}

// ---- Gallery: templates the current validator and actions can express
// (docs/plan/automations.md §8). Each makes a draft; nothing is saved
// until the user saves it. A step that cannot run unattended waits for
// approval, as the editor shows.

export interface Template {
  id: string;
  /** What it needs to make sense here: Docker, a battery. */
  needs?: 'docker' | 'battery';
  make: (text: (key: MessageKey) => string) => Draft;
}

const tpl = (id: string, trigger: Trigger, then: Step[], options: Partial<Options> = {}): ((text: (key: MessageKey) => string) => Draft) => (text) => ({
  name: text(`auto.tpl.${id}` as MessageKey),
  enabled: true,
  origin: `template:${id}`,
  spec: { trigger, then: then.map((s) => (s.step === 'notify' ? { ...s, text: text(`auto.tpl.${id}.text` as MessageKey) } : s)), options: { ...DEFAULT_OPTIONS, ...options } },
});

const notify = (): Step => ({ step: 'notify', channels: ['desktop'], text: '' });

export const TEMPLATES: readonly Template[] = [
  { id: 'heat', make: tpl('heat', { kind: 'metric', metric: 'cpu.temp', op: '>', threshold: 90, for_s: 60 }, [{ step: 'action', action: 'power/profile', target: 'power-saver' }, notify()]) },
  { id: 'unit-failed', make: tpl('unit-failed', { kind: 'unit_failed', scope: 'user' }, [notify()]) },
  { id: 'container-unhealthy', needs: 'docker', make: tpl('container-unhealthy', { kind: 'container', event: 'unhealthy' }, [notify()], { max_per_hour: 3 }) },
  { id: 'exposure', make: tpl('exposure', { kind: 'new_listener' }, [notify()]) },
  { id: 'ssh', make: tpl('ssh', { kind: 'ssh_failed', count: 10 }, [notify()]) },
  { id: 'integrity', make: tpl('integrity', { kind: 'integrity_changed' }, [notify()]) },
  { id: 'usb', make: tpl('usb', { kind: 'usb_device' }, [notify()]) },
  { id: 'security-updates', make: tpl('security-updates', { kind: 'security_updates' }, [notify()]) },
  { id: 'unplugged', needs: 'battery', make: tpl('unplugged', { kind: 'power', event: 'unplugged' }, [{ step: 'action', action: 'power/profile', target: 'power-saver' }]) },
  { id: 'plugged', needs: 'battery', make: tpl('plugged', { kind: 'power', event: 'plugged' }, [{ step: 'action', action: 'power/profile', target: 'balanced' }]) },
  { id: 'disk-full', make: tpl('disk-full', { kind: 'metric', metric: 'disk.used_pct', op: '>', threshold: 90, for_s: 300 }, [{ step: 'action', action: 'space/preview', target: 'space' }, notify()]) },
  { id: 'internet-back', make: tpl('internet-back', { kind: 'internet', state: 'back' }, [notify()]) },
];

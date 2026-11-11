import { describe, expect, it } from 'vitest';
import en from './i18n/en.json';
import {
  RUN_STATES,
  STEP_RESULTS,
  TRIGGERS,
  reasonKey,
  rowKind,
  runStateKey,
  status,
  stepResultKey,
  stepWords,
  triggerWords,
  type Automation,
  type Trigger,
} from './automations';

const keys = en as Record<string, string>;
const spec = (trigger: Trigger) => ({ trigger, then: [{ step: 'wait' as const, seconds: 1 }], options: { cooldown_s: 300, max_per_hour: 6 } });
const auto = (over: Partial<Automation>): Automation => ({ id: 1, name: 'x', enabled: true, origin: 'ui', created: 0, updated: 0, spec: spec({ kind: 'login' }), ...over });

describe('automations', () => {
  it('words every trigger kind and variant with a key that exists', () => {
    const variants: Trigger[] = [];
    for (const kind of TRIGGERS) variants.push({ kind });
    for (const event of ['died', 'unhealthy', 'oom']) variants.push({ kind: 'container', event });
    for (const kind of ['wifi', 'vpn']) for (const state of ['up', 'down']) variants.push({ kind, state, uuid: 'u' });
    variants.push({ kind: 'internet', state: 'back' }, { kind: 'power', event: 'plugged' }, { kind: 'display', event: 'disconnected' });
    variants.push({ kind: 'app', game: true, state: 'stopped' }, { kind: 'app', app: 'org.x', state: 'stopped' }, { kind: 'files', event: 'older_than', days: 3 });
    variants.push({ kind: 'nonsense' }, { kind: 'metric', op: '<', for_s: 5 }, { kind: 'metric', op: '>' });
    for (const v of variants) expect(keys, JSON.stringify(v)).toHaveProperty([triggerWords(v).key]);
  });

  it('passes names as values, never as markup', () => {
    const w = triggerWords({ kind: 'wifi', state: 'up', uuid: 'abc' }, { connection: (u) => (u === 'abc' ? '<b>Home</b>' : u) });
    expect(w.values).toEqual({ s: '<b>Home</b>' });
    const m = triggerWords({ kind: 'metric', metric: 'cpu.temp', op: '>', threshold: 90, for_s: 60 }, { metric: () => 'CPU temperature' });
    expect(m).toEqual({ key: 'auto.trig.metric.aboveFor', values: { metric: 'CPU temperature', n: 90, target: '', s: 60 } });
    expect(triggerWords({ kind: 'metric', metric: 'cpu.temp', op: '<', threshold: 5 }).key).toBe('auto.trig.metric.below');
  });

  it('tells rows apart: a mode trigger, an alert rule, an invalid spec', () => {
    expect(rowKind(auto({ origin: 'mode:away#0@abc' }))).toBe('mode');
    expect(rowKind(auto({ spec: spec({ kind: 'alert', metric: 'cpu.temp' }) }))).toBe('alert');
    const noSpec: Automation = { id: 2, name: 'y', enabled: false, origin: 'ui', created: 0, updated: 0, invalid: 'bad' };
    expect(rowKind(noSpec)).toBe('invalid');
    expect(rowKind(auto({}))).toBe('plain');
  });

  it('shows the status that matters most', () => {
    expect(status(auto({ enabled: false }), true)).toBe('off');
    expect(status(auto({ waiting: 2 }), true)).toBe('waiting');
    expect(status(auto({}), true)).toBe('paused');
    expect(status(auto({}), false)).toBe('on');
  });

  it('words run states, step results and the engine reasons', () => {
    for (const s of RUN_STATES) expect(keys).toHaveProperty([runStateKey(s)]);
    for (const s of STEP_RESULTS) expect(keys).toHaveProperty([stepResultKey(s)]);
    expect(runStateKey('weird')).toBe('auto.run.other');
    expect(reasonKey('cooldown')).toBe('auto.why.cooldown');
    expect(reasonKey('something else')).toBeNull();
  });

  it('words steps, a label first', () => {
    expect(stepWords({ step: 'action', action: 'power/profile', target: 'power-saver', label: 'Cool down' })).toEqual({ key: 'auto.stepLabel', values: { s: 'Cool down' } });
    expect(stepWords({ step: 'wait', seconds: 30 })).toEqual({ key: 'auto.stepWait', values: { n: 30 } });
  });
});

import {
  CATALOG,
  CONDITIONS as CONDS,
  NEW_TRIGGERS,
  TEMPLATES,
  actionKey,
  blankCondition,
  blankStep,
  blankTrigger,
  body,
  draftOf,
  moved,
  offered,
} from './automations';

describe('automation editor', () => {
  it('has words for every action, fixed target, trigger kind, condition and template', () => {
    for (const c of CATALOG) {
      expect(keys).toHaveProperty([actionKey(c.key)]);
      for (const o of c.options ?? []) expect(keys).toHaveProperty([`auto.target.${o}`]);
    }
    expect(keys).toHaveProperty([actionKey('some/thing')]);
    for (const k of TRIGGERS) expect(keys).toHaveProperty([`auto.kind.${k}`]);
    for (const k of CONDS) expect(keys).toHaveProperty([`auto.cond.${k}`]);
    for (const tp of TEMPLATES) {
      expect(keys).toHaveProperty([`auto.tpl.${tp.id}`]);
      expect(keys).toHaveProperty([`auto.tpl.${tp.id}.about`]);
    }
    // The dry run's codes (internal/automations/dryrun.go).
    for (const n of ['exact', 'boots', 'outages', 'alert-own', 'alert-watched', 'minute', 'rounded', 'disk-size', 'since', 'no-data', 'unreadable', 'not-recorded', 'not-yet'])
      expect(keys).toHaveProperty([`auto.dry.note.${n}`]);
    for (const w of ['quiet', 'cooldown', 'hourly', 'conditions']) expect(keys).toHaveProperty([`auto.dry.why.${w}`]);
    for (const w of ['earlier-waits', 'no-actions']) expect(keys).toHaveProperty([`auto.out.why.${w}`]);
  });

  it('offers only catalog actions the server has', () => {
    expect(offered([{ key: 'power/profile', risk: 'safe' }, { key: 'proc/kill', risk: 'destructive' }]).map((c) => c.key)).toEqual(['power/profile']);
  });

  it('never offers an alert rule or a folder for a new automation', () => {
    expect(NEW_TRIGGERS).not.toContain('alert');
    expect(NEW_TRIGGERS).not.toContain('files');
    for (const k of NEW_TRIGGERS) expect(blankTrigger(k).kind).toBe(k);
    for (const k of CONDS) expect(blankCondition(k).kind).toBe(k);
    expect(blankStep('action', CATALOG[0])).toEqual({ step: 'action', action: 'power/profile', target: 'power-saver' });
  });

  it('keeps every field when an automation is opened and saved, the ones it does not show too', () => {
    const spec = {
      trigger: { kind: 'alert', metric: 'disk.used_pct', op: '>', threshold: 90, for_s: 60, target: '/', severity: 'critical' },
      then: [
        { step: 'notify' as const, channels: ['all'], text: '{alert}', severity: 'critical' },
        { step: 'action' as const, action: 'thing/unlisted', target: 't', params: { a: '1' }, continue: true },
        { step: 'check' as const, what: 'unit_state', target: 'user/x.service', expect: 'active', then: [{ step: 'wait' as const, seconds: 5 }], else: [{ step: 'notify' as const, channels: ['desktop'], text: 'no' }] },
      ],
      options: { cooldown_s: 60, max_per_hour: 60, quiet_from: '22:00', quiet_to: '07:00' },
    };
    const a: Automation = { id: 4, name: 'Disk', enabled: true, origin: 'alert', created: 0, updated: 0, spec };
    const d = draftOf(a);
    if (!d) throw new Error('no draft');
    expect(JSON.parse(body(d))).toEqual({ name: 'Disk', enabled: true, spec });
    // A copy: editing the draft leaves the list's object alone.
    d.spec.then.pop();
    expect(spec.then).toHaveLength(3);
  });

  it('makes drafts from templates, with a template origin only when new', () => {
    for (const tp of TEMPLATES) {
      const d = tp.make((k) => keys[k] ?? k);
      expect(d.origin).toBe(`template:${tp.id}`);
      expect(d.name).not.toBe('');
      for (const st of d.spec.then) if (st.step === 'notify') expect(st.text).not.toBe('');
      expect(JSON.parse(body(d)).origin).toBe(`template:${tp.id}`);
      expect(JSON.parse(body({ ...d, id: 3 })).origin).toBeUndefined();
    }
  });

  it('moves a step one place, never out of the list', () => {
    expect(moved(['a', 'b', 'c'], 0, 1)).toEqual(['b', 'a', 'c']);
    expect(moved(['a', 'b'], 0, -1)).toEqual(['a', 'b']);
  });
});

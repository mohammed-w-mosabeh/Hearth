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
    variants.push({ kind: 'nonsense' });
    for (const v of variants) expect(keys, JSON.stringify(v)).toHaveProperty([triggerWords(v).key]);
  });

  it('passes names as values, never as markup', () => {
    const w = triggerWords({ kind: 'wifi', state: 'up', uuid: 'abc' }, { connection: (u) => (u === 'abc' ? '<b>Home</b>' : u) });
    expect(w.values).toEqual({ s: '<b>Home</b>' });
    const m = triggerWords({ kind: 'metric', metric: 'cpu.temp', op: '>', threshold: 90, for_s: 60 }, { metric: () => 'CPU temperature' });
    expect(m).toEqual({ key: 'auto.trig.metric', values: { metric: 'CPU temperature', op: '>', n: 90, target: '', s: 60 } });
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

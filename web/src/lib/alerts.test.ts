import { describe, expect, it } from 'vitest';
import { applyEvent, blankRule, draftOf, conditionText, forText, lasted, ruleError, valueText, type AlertEvent, type Metric } from './alerts';

const disk: Metric = { id: 'disk.used_pct', label: 'Disk space used', unit: '%', instance: 'mount', hysteresis: 2, min: 0, max: 100, instances: ['/'] };
const temp: Metric = { id: 'cpu.temp', label: 'CPU temperature', unit: '°C', hysteresis: 5, min: 0, max: 150, instances: [] };
const unit: Metric = { id: 'unit.failed', label: 'Unit failed', unit: '', instance: 'unit', hysteresis: 0, min: 0, max: 1, instances: [] };

const ev = (id: number, extra: Partial<AlertEvent> = {}): AlertEvent => ({
  id, rule_id: 1, rule_name: 'r', metric: 'cpu.usage', instance: '', severity: 'warning', op: '>', threshold: 90,
  value: 95, peak: 97, started_at: 0, notified: true, ...extra,
});

describe('alerts text', () => {
  it('describes durations, values and conditions', () => {
    expect(forText(0)).toBe('at once');
    expect(forText(45)).toBe('for 45 s');
    expect(forText(180)).toBe('for 3 min');
    expect(forText(5400)).toBe('for 1 h 30 min');
    expect(valueText(90.4, disk)).toBe('90%');
    expect(valueText(91, temp)).toBe('91 °C');
    expect(conditionText({ metric: 'disk.used_pct', target: '', op: '>', threshold: 90, for_s: 60 }, disk)).toBe('Disk space used (any mount) > 90% for 1 min');
    expect(conditionText({ metric: 'disk.used_pct', target: '/home', op: '>', threshold: 80, for_s: 0 }, disk)).toBe('Disk space used (/home) > 80%');
    expect(conditionText({ metric: 'cpu.temp', target: '', op: '>', threshold: 90, for_s: 60 }, temp)).toBe('CPU temperature > 90 °C for 1 min');
    expect(conditionText({ metric: 'unit.failed', target: '', op: '>', threshold: 0, for_s: 0 }, unit)).toBe('Any unit failed');
  });
  it('validates drafts', () => {
    const r = { ...blankRule(disk), name: 'Full' };
    expect(ruleError(r, disk)).toBeNull();
    expect(ruleError({ ...r, name: ' ' }, disk)).toMatch(/name/);
    expect(ruleError({ ...r, threshold: 120 }, disk)).toMatch(/0 to 100/);
    expect(ruleError({ ...r, for_s: 1.5 }, disk)).toMatch(/duration/);
    expect(ruleError({ ...r, quiet_from: '22:00' }, disk)).toMatch(/both/);
    expect(ruleError({ ...r, quiet_from: '22:00', quiet_to: '22:00' }, disk)).toMatch(/different/);
    expect(ruleError(r, undefined)).toMatch(/watch/);
    expect(blankRule(unit).threshold).toBe(0);
    expect(draftOf({ id: 7, ...r })).toEqual(r);
  });
  it('measures incidents and applies live updates', () => {
    expect(lasted(ev(1, { started_at: 0, ended_at: 90_000 }))).toBe('2 min');
    expect(lasted(ev(1, { started_at: 0 }), 30_000)).toBe('30 s');
    const list = [ev(2), ev(1)];
    expect(applyEvent(list, ev(3)).map((e) => e.id)).toEqual([3, 2, 1]);
    const closed = applyEvent(list, ev(2, { ended_at: 5 }));
    expect(closed.map((e) => e.id)).toEqual([2, 1]);
    expect(closed[0]?.ended_at).toBe(5);
  });
});

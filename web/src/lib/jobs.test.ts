import { describe as group, expect, it } from 'vitest';
import { blank, describe, fromCalendar, jobError, order, toCalendar, type Builder, type Listed } from './jobs';

group('builder', () => {
  const cases: [Builder, string][] = [
    [{ freq: 'minutes', every: 15 }, '*:0/15'],
    [{ freq: 'hourly', minute: 5 }, '*-*-* *:05:00'],
    [{ freq: 'daily', time: '03:30' }, '*-*-* 03:30:00'],
    [{ freq: 'weekly', days: ['Fri', 'Mon'], time: '09:00' }, 'Mon,Fri *-*-* 09:00:00'],
    [{ freq: 'monthly', day: 1, time: '00:00' }, '*-*-01 00:00:00'],
  ];
  it('writes OnCalendar and reads it back', () => {
    for (const [b, expr] of cases) {
      expect(toCalendar(b)).toBe(expr);
      const back = fromCalendar(expr);
      expect(toCalendar(back)).toBe(expr);
    }
    expect(fromCalendar('Mon,Fri *-*-* 09:00:00')).toEqual({ freq: 'weekly', days: ['Mon', 'Fri'], time: '09:00' });
  });
  it('keeps anything else as a custom expression', () => {
    for (const expr of ['daily', 'Mon..Fri 09:30', '*-*-31 00:00:00', 'Xyz *-*-* 09:00:00', '2026-12-24 18:00']) {
      expect(fromCalendar(expr)).toEqual({ freq: 'custom', expr });
    }
  });
  it('refuses incomplete states', () => {
    expect(toCalendar({ freq: 'minutes', every: 0 })).toBeNull();
    expect(toCalendar({ freq: 'daily', time: '25:00' })).toBeNull();
    expect(toCalendar({ freq: 'weekly', days: [], time: '09:00' })).toBeNull();
    expect(toCalendar({ freq: 'monthly', day: 31, time: '09:00' })).toBeNull();
    expect(toCalendar({ freq: 'custom', expr: '  ' })).toBeNull();
  });
});

group('describe', () => {
  it('puts schedules in words', () => {
    expect(describe({ kind: 'calendar', value: '*:0/15' })).toBe('every 15 minutes');
    expect(describe({ kind: 'calendar', value: '*-*-* 03:30:00' })).toBe('daily at 03:30');
    expect(describe({ kind: 'calendar', value: 'Mon,Fri *-*-* 09:00:00' })).toBe('Mon, Fri at 09:00');
    expect(describe({ kind: 'calendar', value: 'Mon..Fri 09:30' })).toBe('Mon..Fri 09:30');
    expect(describe({ kind: 'boot', value: '5min' })).toBe('5min after boot');
    expect(describe({ kind: 'interval', value: '6h' })).toBe('every 6h');
  });
});

group('jobError', () => {
  it('mirrors the server checks', () => {
    const j = { ...blank(), name: 'Backup' };
    expect(jobError(j)).toBeNull();
    expect(jobError({ ...j, name: ' Backup' })).toBe('name');
    expect(jobError({ ...j, name: 'a\nb' })).toBe('name');
    expect(jobError({ ...j, name: 'a\\' })).toBe('name');
    expect(jobError({ ...j, script: 'echo hi' })).toBe('script');
    expect(jobError({ ...j, timeout: 0 })).toBe('timeout');
    expect(jobError({ ...j, schedule: { kind: 'calendar', value: ' ' } })).toBe('schedule');
  });
});

group('order', () => {
  it('puts failed and running jobs first, then by next run', () => {
    const l = (name: string, x: Partial<Listed>): Listed => ({ ...blank(), id: name, name, scope: 'user', paused: false, running: false, failed: false, ...x });
    const got = order([l('later', { next: 200 }), l('paused', { paused: true }), l('soon', { next: 100 }), l('broken', { failed: true }), l('busy', { running: true })]);
    expect(got.map((j) => j.name)).toEqual(['broken', 'busy', 'soon', 'later', 'paused']);
  });
});

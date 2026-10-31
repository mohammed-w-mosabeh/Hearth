import { describe, expect, it } from 'vitest';
import { days, pollMs, verdict, worrying, type SmartDisk } from './smart';

const disk = (o: Partial<SmartDisk>): SmartDisk => ({ device: 'sda', supported: true, failing: false, testing: false, at: 1, ...o });

describe('smart', () => {
  it('gives each disk one verdict', () => {
    expect(verdict(disk({}))).toBe('ok');
    expect(verdict(disk({ failing: true }))).toBe('failing');
    expect(verdict(disk({ supported: false }))).toBe('unsupported');
    expect(verdict(disk({ at: 0, supported: false, asleep: true }))).toBe('asleep');
    expect(verdict(disk({ at: 0, supported: false }))).toBe('unknown');
  });
  it('keeps a failing disk failing while it sleeps', () => {
    expect(verdict(disk({ failing: true, asleep: true }))).toBe('failing');
    expect(verdict(disk({ asleep: true }))).toBe('ok');
  });
  it('flags only figures above zero', () => {
    expect(worrying(undefined)).toBe(false);
    expect(worrying(0)).toBe(false);
    expect(worrying(2)).toBe(true);
  });
  it('rounds days down', () => {
    expect(days(47)).toBe(1);
    expect(days(48)).toBe(2);
  });
  it('polls only during a test', () => {
    expect(pollMs([disk({})])).toBeNull();
    expect(pollMs([disk({}), disk({ testing: true })])).toBe(30000);
  });
});

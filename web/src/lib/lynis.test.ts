import { describe, expect, it } from 'vitest';
import { lynisPage, lynisPoints, lynisTrend, type LynisRun } from './lynis';

const run = (time: number, index: number): LynisRun => ({ time, index, warnings: 0, suggestions: 0 });

describe('lynisPage', () => {
  it('links a known group to its page', () => {
    expect(lynisPage('PKGS-7392')).toBe('#/packages');
    expect(lynisPage('FIRE-4590')).toBe('#/network');
    expect(lynisPage('SSH-7408')).toBe('#/security');
  });
  it('links nothing it does not know', () => {
    expect(lynisPage('HRDN-7222')).toBe('');
    expect(lynisPage('')).toBe('');
    expect(lynisPage('constructor-1')).toBe('');
    expect(lynisPage('__proto__-1')).toBe('');
  });
});

describe('lynisTrend', () => {
  it('compares the last two runs', () => {
    expect(lynisTrend([run(2, 60), run(1, 65)])).toBe(-5);
    expect(lynisTrend([run(1, 60)])).toBeNull();
    expect(lynisTrend([])).toBeNull();
  });
});

describe('lynisPoints', () => {
  it('draws oldest first, with 100 at the top', () => {
    expect(lynisPoints([run(2, 100), run(1, 0)], 100, 40)).toBe('0.0,40.0 100.0,0.0');
  });
  it('needs two runs for a line', () => {
    expect(lynisPoints([run(1, 50)], 100, 40)).toBe('');
  });
});

import { describe, expect, it } from 'vitest';
import { append, matches, pick } from './history';

describe('history columns', () => {
  it('matches selectors like the server', () => {
    expect(matches('cpu.core.3', ['cpu.core.'])).toBe(true);
    expect(matches('net.rx:wlp3s0', ['net.rx:'])).toBe(true);
    expect(matches('net.rx', ['net.rx:'])).toBe(false);
    expect(matches('cpu.usage2', ['cpu.usage'])).toBe(false);
  });

  it('appends only newer points and trims', () => {
    const base = { t: [1000, 2000], series: { a: [1, 2] } };
    const add = { t: [2000, 3000, 4000], series: { a: [9, 3, 4], b: [null, 30, 40] } };
    expect(append(base, add, 0)).toEqual({ t: [1000, 2000, 3000, 4000], series: { a: [1, 2, 3, 4], b: [null, null, 30, 40] } });
    expect(append(base, add, 2500)).toEqual({ t: [3000, 4000], series: { a: [3, 4], b: [30, 40] } });
    expect(append(base, { t: [], series: {} }, 0)).toEqual(base);
  });

  it('picks non-empty selected series in natural order', () => {
    const cols = { t: [1], series: { 'cpu.core.10': [1], 'cpu.core.2': [1], 'cpu.core.3': [null], 'cpu.usage': [1] } };
    expect(pick(cols, ['cpu.core.'])).toEqual(['cpu.core.2', 'cpu.core.10']);
    expect(pick(cols, ['cpu.usage', 'cpu.core.'])).toEqual(['cpu.usage', 'cpu.core.2', 'cpu.core.10']);
  });
});

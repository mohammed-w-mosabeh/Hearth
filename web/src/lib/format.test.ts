import { describe, expect, it } from 'vitest';
import { bytes, celsius, duration, pct, rate } from './format';

describe('format', () => {
  it('bytes', () => {
    expect(bytes(0)).toBe('0 B');
    expect(bytes(1023)).toBe('1023 B');
    expect(bytes(1536)).toBe('1.5 KiB');
    expect(bytes(16000000 * 1024)).toBe('15.3 GiB');
    expect(bytes(500 * 1024 ** 3)).toBe('500 GiB');
    expect(bytes(-1)).toBe('–');
    expect(bytes(NaN)).toBe('–');
  });
  it('rate, pct, celsius', () => {
    expect(rate(2048)).toBe('2.0 KiB/s');
    expect(pct(49.6)).toBe('50%');
    expect(pct(49.64, 1)).toBe('49.6%');
    expect(celsius(61.4)).toBe('61°C');
  });
  it('duration', () => {
    expect(duration(8160)).toBe('2 h 16 min');
    expect(duration(59)).toBe('< 1 min');
    expect(duration(600)).toBe('10 min');
    expect(duration(0)).toBe('–');
  });
});

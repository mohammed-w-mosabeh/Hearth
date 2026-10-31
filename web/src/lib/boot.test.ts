import { describe, expect, it } from 'vitest';
import { PHASES, phaseKey, slowLink, spanText } from './boot';
import en from './i18n/en.json';

describe('boot', () => {
  it('has a message for every phase', () => {
    for (const p of PHASES) expect(en).toHaveProperty([phaseKey(p)]);
  });
  it('words a span', () => {
    expect(spanText(189)).toBe('189 ms');
    expect(spanText(26127)).toBe('26.1 s');
    expect(spanText(126668)).toBe('2 min 7 s');
  });
  it('marks the slowest step', () => {
    const chain = [{ name: 'a', at_ms: 0, ms: 0 }, { name: 'b', at_ms: 0, ms: 5 }, { name: 'c', at_ms: 0, ms: 9 }];
    expect(chain.map((l) => slowLink(l, chain))).toEqual([false, false, true]);
  });
});

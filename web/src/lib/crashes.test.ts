import { describe, expect, it } from 'vitest';
import en from './i18n/en.json';
import { ownerKey, peakDay } from './crashes';

describe('crashes', () => {
  it('names owners', () => {
    for (const kind of ['unit', 'container', 'app']) expect(en).toHaveProperty([ownerKey({ kind, name: 'x' }) ?? '']);
    expect(ownerKey({ kind: 'future', name: 'x' })).toBeNull();
  });
  it('scales to the tallest day', () => {
    expect(peakDay([])).toBe(1);
    expect(peakDay([{ day: 'a', crash: 2, oom: 3 }, { day: 'b', crash: 1, oom: 0 }])).toBe(5);
  });
});

import { describe, expect, it } from 'vitest';
import { loadDensity, saveDensity } from './density';

const memory = () => {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
};

describe('density', () => {
  it('defaults to comfortable and remembers', () => {
    const s = memory();
    expect(loadDensity(s)).toBe('comfortable');
    saveDensity('compact', s);
    expect(loadDensity(s)).toBe('compact');
  });
  it('ignores what it does not know', () => {
    expect(loadDensity({ getItem: () => 'huge' })).toBe('comfortable');
  });
  it('works when storage throws', () => {
    const blocked = () => {
      throw new Error('blocked');
    };
    expect(loadDensity({ getItem: blocked })).toBe('comfortable');
    expect(() => saveDensity('compact', { setItem: blocked })).not.toThrow();
  });
});

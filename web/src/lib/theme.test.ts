import { describe, expect, it } from 'vitest';
import { loadTheme, resolveTheme, saveTheme } from './theme';

const memory = () => {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
};

describe('theme', () => {
  it('defaults to dark and remembers', () => {
    const s = memory();
    expect(loadTheme(s)).toBe('dark');
    saveTheme('light', s);
    expect(loadTheme(s)).toBe('light');
    saveTheme('system', s);
    expect(loadTheme(s)).toBe('system');
  });
  it('ignores what it does not know', () => {
    expect(loadTheme({ getItem: () => 'neon' })).toBe('dark');
  });
  it('works when storage throws', () => {
    const blocked = () => {
      throw new Error('blocked');
    };
    expect(loadTheme({ getItem: blocked })).toBe('dark');
    expect(() => saveTheme('light', { setItem: blocked })).not.toThrow();
  });
  it('resolves the system choice', () => {
    expect(resolveTheme('system', true)).toBe('dark');
    expect(resolveTheme('system', false)).toBe('light');
    expect(resolveTheme('light', true)).toBe('light');
    expect(resolveTheme('dark', false)).toBe('dark');
  });
});

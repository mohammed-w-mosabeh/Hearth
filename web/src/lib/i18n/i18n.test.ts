import { afterEach, describe, expect, it, vi } from 'vitest';
import ar from './ar.json';
import en from './en.json';
import { applyLang, isRTL, loadLang, locale, ltr, menuLeft, saveLang, t } from './index';

describe('t', () => {
  it('fills placeholders and leaves unknown ones', () => {
    expect(t('svc.count', { n: 2 })).toBe('2 units.');
    expect(t('svc.count')).toBe('{n} units.');
  });

  it('has no empty messages', () => {
    for (const [k, v] of Object.entries(en)) expect(v, k).not.toBe('');
  });
});

const root = {} as HTMLElement; // these tests run without a document
const holes = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();

describe('Arabic', () => {
  afterEach(() => applyLang('en', root));

  it('has exactly the keys of English, with the same placeholders', () => {
    expect(Object.keys(ar).sort()).toEqual(Object.keys(en).sort());
    const messages: Record<string, string> = ar;
    for (const [k, v] of Object.entries(en)) expect(holes(messages[k] ?? ''), k).toEqual(holes(v));
  });

  it('has no empty messages and nothing left in English by accident', () => {
    const same = Object.entries(ar).filter(([k, v]) => v === (en as Record<string, string>)[k] && /[a-z]{4} [a-z]{2}/.test(v));
    expect(same).toEqual([]);
    for (const [k, v] of Object.entries(ar)) expect(v, k).not.toBe('');
  });

  it('sets the direction, and goes back', async () => {
    await applyLang('ar', root);
    expect([root.lang, root.dir, isRTL()]).toEqual(['ar', 'rtl', true]);
    expect(t('nav.home')).toBe(ar['nav.home']);
    await applyLang('en', root);
    expect([root.lang, root.dir, isRTL()]).toEqual(['en', 'ltr', false]);
  });

  it('keeps Western digits and the Gregorian calendar', async () => {
    await applyLang('ar', root);
    const when = new Date('2026-10-31T12:00:00Z').toLocaleDateString(locale(), { timeZone: 'UTC' });
    expect(when).toContain('2026');
    expect((1234).toLocaleString(locale())).not.toMatch(/[٠-٩]/);
  });

  it('isolates left-to-right runs only in Arabic', async () => {
    expect(ltr('1 KiB')).toBe('1 KiB');
    await applyLang('ar', root);
    expect(ltr('1 KiB')).toBe('\u20661 KiB\u2069');
  });

  it('isolates every value, and a hostile one stays inside', async () => {
    await applyLang('ar', root);
    expect(t('svc.done.start', { name: '/x\u202e' })).toContain('\u2068/x\u202e\u2069');
  });

  it('lines a menu up with the outer edge of its button', async () => {
    vi.stubGlobal('innerWidth', 1000);
    const b = { left: 100, right: 130 } as DOMRect;
    expect(menuLeft(b, 50)).toBe(80);
    await applyLang('ar', root);
    expect(menuLeft(b, 50)).toBe(100);
  });
});

describe('language choice', () => {
  const memory = (seed: string | null = null) => {
    let v = seed;
    return { getItem: () => v, setItem: (_: string, x: string) => void (v = x) };
  };
  it('defaults to English, even when storage is blocked', () => {
    expect(loadLang(memory())).toBe('en');
    expect(loadLang(memory('xx'))).toBe('en');
    expect(loadLang({ getItem: () => { throw new Error('blocked'); } })).toBe('en');
  });
  it('remembers Arabic', () => {
    const m = memory();
    saveLang('ar', m);
    expect(loadLang(m)).toBe('ar');
  });
});

import { describe, expect, it } from 'vitest';
import { GOTO, SEQUENCE_MS, Sequence, ignored, letter, type Keypress } from './shortcuts';

const press = (key: string, over: Partial<Keypress> = {}): Keypress => ({
  key, code: `Key${key.toUpperCase()}`, ctrlKey: false, metaKey: false, altKey: false, target: null, ...over,
});

describe('shortcuts', () => {
  it('goes to a page with g then a letter', () => {
    const s = new Sequence();
    expect(s.press(press('g'), 0)).toBeNull();
    expect(s.press(press('d'), 100)).toEqual({ go: 'docker' });
    expect(s.press(press('d'), 200)).toBeNull();
  });
  it('forgets g after a while, and after a wrong letter', () => {
    const s = new Sequence();
    s.press(press('g'), 0);
    expect(s.press(press('d'), SEQUENCE_MS + 1)).toBeNull();
    s.press(press('g'), 0);
    expect(s.press(press('z'), 1)).toBeNull();
    expect(s.press(press('d'), 2)).toBeNull();
  });
  it('opens help', () => {
    expect(new Sequence().press(press('?', { code: 'Slash' }), 0)).toEqual({ help: true });
  });
  it('leaves modified keys alone', () => {
    const s = new Sequence();
    expect(s.press(press('g', { ctrlKey: true }), 0)).toBeNull();
    expect(s.press(press('d'), 1)).toBeNull();
    expect(ignored(press('g', { metaKey: true }))).toBe(true);
    expect(ignored(press('g', { altKey: true }))).toBe(true);
  });
  it('leaves typing, terminals and dialogs alone', () => {
    const inside = { closest: () => ({}) } as unknown as EventTarget;
    const outside = { closest: () => null } as unknown as EventTarget;
    expect(ignored(press('g', { target: inside }))).toBe(true);
    expect(ignored(press('g', { target: outside }))).toBe(false);
    const s = new Sequence();
    s.press(press('g'), 0);
    expect(s.press(press('d', { target: inside }), 1)).toBeNull();
  });
  it('works on an Arabic layout', () => {
    expect(letter({ key: 'ل', code: 'KeyG' })).toBe('g');
    expect(letter({ key: '؟', code: 'Slash' })).toBe('?');
    const s = new Sequence();
    s.press(press('ل', { code: 'KeyG' }), 0);
    expect(s.press(press('ي', { code: 'KeyD' }), 1)).toEqual({ go: 'docker' });
  });
  it('has no letter twice and no unknown page', () => {
    expect(new Set(Object.values(GOTO)).size).toBe(Object.keys(GOTO).length);
    expect(Object.keys(GOTO).every((k) => /^[a-z]$/.test(k) && k !== 'g')).toBe(true);
  });
});

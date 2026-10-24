import { describe, expect, it } from 'vitest';
import { arrange, moveTo, sanitize, setHidden, step } from './layout';

describe('layout', () => {
  it('sanitizes untrusted data', () => {
    expect(sanitize(null)).toEqual({ order: [], hidden: [] });
    expect(sanitize('x')).toEqual({ order: [], hidden: [] });
    expect(sanitize({ order: ['cpu', 'cpu', '<b>', 3, 'disk:/home'], hidden: 'mem' })).toEqual({
      order: ['cpu', 'disk:/home'],
      hidden: [],
    });
  });

  it('arranges available cards by the saved order', () => {
    const l = { order: ['mem', 'disk:/media/usb', 'cpu'], hidden: ['temps'] };
    expect(arrange(l, ['cpu', 'mem', 'temps', 'net'])).toEqual({
      visible: ['mem', 'cpu', 'net'],
      hidden: ['temps'],
    });
  });

  it('moves and keeps absent cards', () => {
    const l = { order: ['cpu', 'disk:/media/usb', 'mem', 'net'], hidden: [] };
    const vis = ['cpu', 'mem', 'net'];
    expect(moveTo(l, vis, 'net', 'cpu').order).toEqual(['net', 'cpu', 'mem', 'disk:/media/usb']);
    expect(moveTo(l, vis, 'cpu', null).order).toEqual(['mem', 'net', 'cpu', 'disk:/media/usb']);
    expect(step(l, vis, 'mem', -1).order).toEqual(['mem', 'cpu', 'net', 'disk:/media/usb']);
    expect(step(l, vis, 'cpu', -1)).toBe(l);
    expect(step(l, vis, 'net', 1)).toBe(l);
  });

  it('hides and shows', () => {
    let l = { order: ['cpu'], hidden: [] as string[] };
    l = setHidden(l, 'gpu:0000:03:00.0', true);
    expect(l).toEqual({ order: ['cpu', 'gpu:0000:03:00.0'], hidden: ['gpu:0000:03:00.0'] });
    expect(setHidden(l, 'gpu:0000:03:00.0', false).hidden).toEqual([]);
  });
});

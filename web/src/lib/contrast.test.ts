import { describe, expect, it } from 'vitest';
import css from '../styles/ember.css?raw';

// WCAG AA on the real token file: 4.5:1 for text, 3:1 for what is not text.

function tokens(selector: string): Record<string, string> {
  const at = css.indexOf(`${selector} {`);
  if (at < 0) throw new Error(`no block ${selector}`);
  const body = css.slice(at, css.indexOf('}', at));
  return Object.fromEntries([...body.matchAll(/--([a-z0-9-]+):\s*(#[0-9a-f]{6});/g)].map((m) => [m[1], m[2]])) as Record<string, string>;
}

function luminance(hex: string): number {
  const channel = (i: number): number => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
}

function ratio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

const dark = tokens(":root[data-ui='ember']");
const themes = { dark, light: { ...dark, ...tokens(":root[data-ui='ember'][data-theme='light']") } };
const backgrounds = ['bg', 'surface', 'surface-2'];
const text = ['text', 'text-muted', 'accent', 'ok', 'warn', 'crit', 'info'];
const series = [1, 2, 3, 4, 5, 6, 7, 8].map((i) => `series-${i}`);

describe('Ember contrast', () => {
  it('computes known ratios', () => {
    expect(ratio('#000000', '#ffffff')).toBeCloseTo(21);
    expect(ratio('#777777', '#ffffff')).toBeCloseTo(4.48, 1);
  });

  for (const [name, t] of Object.entries(themes)) {
    const pair = (fg: string, bg: string, min: number): void => {
      const [a, b] = [t[fg], t[bg]];
      if (!a || !b) throw new Error(`${name}: missing ${fg} or ${bg}`);
      expect(ratio(a, b), `${name}: ${fg} on ${bg}`).toBeGreaterThanOrEqual(min);
    };
    it(`${name}: text and status on every background`, () => {
      for (const bg of backgrounds) for (const fg of text) pair(fg, bg, 4.5);
    });
    it(`${name}: text on the accent and on its soft tint`, () => {
      pair('accent-contrast', 'accent', 4.5);
      pair('text', 'accent-soft', 4.5);
      pair('accent', 'accent-soft', 4.5);
    });
    it(`${name}: focus ring and chart lines`, () => {
      for (const bg of backgrounds) pair('focus', bg, 3);
      for (const s of series) pair(s, 'surface', 3);
    });
  }
});

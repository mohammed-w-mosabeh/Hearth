import { describe, expect, it } from 'vitest';
import css from '../styles/ember.css?raw';
import { lightAnsi } from './termtheme';

function luminance(hex: string): number {
  const channel = (i: number): number => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
}

const ratio = (a: string, b: string): number => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
};

describe('terminal colours on the light theme', () => {
  const block = css.slice(css.indexOf(":root[data-ui='ember'][data-theme='light'] {"));
  const bg = /--bg:\s*(#[0-9a-f]{6});/.exec(block)?.[1] ?? '';

  it('reads the background', () => {
    expect(bg).toMatch(/^#[0-9a-f]{6}$/);
  });

  it('keeps every ANSI colour at AA on it', () => {
    for (const [name, c] of Object.entries(lightAnsi)) expect(ratio(c, bg), name).toBeGreaterThanOrEqual(4.5);
  });
});

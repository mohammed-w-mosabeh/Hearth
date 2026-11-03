import { describe, expect, it } from 'vitest';
import tokens from '../styles/tokens.css?raw';

// Motion (ROADMAP 8.9): transitions of at most 150 ms, nothing animates
// under prefers-reduced-motion.

const sources = import.meta.glob<string>(['../**/*.svelte', '../**/*.css'], { query: '?raw', import: 'default', eager: true });

/** Every transition and animation value, with comments stripped. */
function declarations(css: string): string[] {
  const plain = css.replace(/\/\*[\s\S]*?\*\//g, '');
  return [...plain.matchAll(/\b(?:transition|animation)(?:-duration|-delay)?\s*:\s*([^;}]+)/g)].map((m) => (m[1] ?? '').trim());
}

const ms = (n: string, unit: string): number => Number(n) * (unit === 's' ? 1000 : 1);

describe('motion', () => {
  it('reads the sources', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50);
    expect(Object.values(sources).flatMap(declarations).length).toBeGreaterThan(10);
    expect(declarations(tokens).length).toBeGreaterThan(0);
  });

  it('keeps the motion token at or under 150 ms, and 0 under reduced motion', () => {
    const base = /--motion:\s*(\d+)ms/.exec(tokens);
    expect(Number(base?.[1])).toBeLessThanOrEqual(150);
    const reduced = tokens.slice(tokens.indexOf('prefers-reduced-motion'));
    expect(reduced).toMatch(/--motion:\s*0ms/);
    expect(reduced).toMatch(/animation:\s*none !important/);
    expect(reduced).toMatch(/transition:\s*none !important/);
  });

  it('times every transition and animation within 150 ms', () => {
    const slow: string[] = [];
    for (const [file, css] of Object.entries(sources)) {
      for (const d of declarations(css)) {
        if (/^none\b/.test(d)) continue;
        for (const [, n, unit] of d.matchAll(/(?<![\w-])(\d*\.?\d+)(ms|s)\b/g)) if (ms(n ?? '0', unit ?? 'ms') > 150) slow.push(`${file}: ${d}`);
      }
    }
    expect(slow).toEqual([]);
  });

  it('never loops an animation', () => {
    const loops = Object.entries(sources).filter(([, css]) => declarations(css).some((d) => /\binfinite\b/.test(d)));
    expect(loops.map(([f]) => f)).toEqual([]);
  });
});

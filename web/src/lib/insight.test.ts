import { describe, expect, it } from 'vitest';
import en from './i18n/en.json';
import { CAUSES, PERIODS, causeKey, periodKey, rounded } from './insight';

describe('insight', () => {
  it('has a message for every key it builds', () => {
    const keys: string[] = [causeKey('future')];
    for (const c of CAUSES) keys.push(causeKey(c));
    for (const p of PERIODS) keys.push(periodKey(p));
    for (const k of keys) expect(en).toHaveProperty([k]);
    expect(causeKey('future')).toBe('insight.cause.other');
  });
  it('rounds estimates', () => {
    expect(rounded(0.04)).toBe('0.0');
    expect(rounded(9.46)).toBe('9.5');
    expect(rounded(123.6)).toBe('124');
  });
});

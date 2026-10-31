import { describe, expect, it } from 'vitest';
import en from './en.json';
import { t } from './index';

describe('t', () => {
  it('fills placeholders and leaves unknown ones', () => {
    expect(t('svc.count', { n: 2 })).toBe('2 units.');
    expect(t('svc.count')).toBe('{n} units.');
  });

  it('has no empty messages', () => {
    for (const [k, v] of Object.entries(en)) expect(v, k).not.toBe('');
  });
});

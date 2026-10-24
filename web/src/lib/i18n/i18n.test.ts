import { describe, expect, it } from 'vitest';
import en from './en.json';
import { t } from './index';

describe('t', () => {
  it('fills placeholders and leaves unknown ones', () => {
    expect(t('page.soon', { page: 'Docker', phase: 2 })).toBe('Docker arrives in phase 2.');
    expect(t('page.soon', { page: 'Docker' })).toBe('Docker arrives in phase {phase}.');
  });

  it('has no empty messages', () => {
    for (const [k, v] of Object.entries(en)) expect(v, k).not.toBe('');
  });
});

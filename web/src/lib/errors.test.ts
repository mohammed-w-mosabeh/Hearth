import { describe, expect, it } from 'vitest';
import { ApiError } from './api';
import { failure, nextStep } from './errors';
import { t } from './i18n';

describe('failure', () => {
  it('says what the server reported, then what to do', () => {
    expect(failure(new ApiError(503, 'hearth-broker is not available'), 'action.failed')).toBe(`hearth-broker is not available. ${t('next.service')}`);
    expect(failure(new ApiError(409, 'the packages changed since the preview; preview again.'), 'action.failed')).toBe(
      `the packages changed since the preview; preview again. ${t('next.conflict')}`,
    );
  });

  it('says Hearth did not answer when the request got no response', () => {
    expect(failure(new TypeError('Failed to fetch'), 'journal.error')).toBe(`${t('journal.error')} ${t('next.offline')}`);
  });

  it('points at the log for anything else', () => {
    expect(failure(new SyntaxError('bad JSON'), 'journal.error')).toBe(`${t('journal.error')} ${t('next.log')}`);
    expect(nextStep(500)).toBe('next.log');
    expect(nextStep(418)).toBe('next.log');
  });

  it('has a next step for every status the server sends', () => {
    for (const s of [0, 400, 403, 404, 409, 413, 422, 429, 500, 502, 503, 504]) expect(t(nextStep(s)), String(s)).toMatch(/\.$/);
  });
});

// Every error says what failed and what to do next (ROADMAP 8.9): a server
// message or an error string goes through failure(), never straight to the UI.
const sources = import.meta.glob<string>(['../**/*.svelte', '../**/*.ts', '!../**/*.test.ts', '!./errors.ts'], {
  query: '?raw',
  import: 'default',
  eager: true,
});

describe('error states', () => {
  it('reads the sources', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(100);
  });

  it('never shows a bare server message', () => {
    const bare = Object.entries(sources).filter(([, s]) => /instanceof ApiError \? \w+\.message/.test(s));
    expect(bare.map(([f]) => f)).toEqual([]);
  });

  it('never renders a load error without its next step', () => {
    const direct = Object.entries(sources).flatMap(([f, s]) => [...s.matchAll(/\{t\('([\w.]*\.error)'\)\}/g)].map((m) => `${f}: ${m[1]}`));
    expect(direct).toEqual([]);
  });
});

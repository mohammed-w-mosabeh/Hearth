import { describe, expect, it } from 'vitest';
import { MAX_ENTRIES, appendEntries, entryTime, journalQuery, matches, type JournalEntry, journalPageQuery, prioClass, type JournalFilter } from './journal';

const e = (cursor: string, prio = 6, msg = 'hello', ident = 'cupsd'): JournalEntry => ({ cursor, t: 0, prio, msg, ident });

describe('journal', () => {
  it('appends without duplicates and caps the length', () => {
    const a = appendEntries([e('1'), e('2')], [e('2'), e('3')]);
    expect(a.map((x) => x.cursor)).toEqual(['1', '2', '3']);
    const many = Array.from({ length: MAX_ENTRIES }, (_, i) => e(String(i)));
    const b = appendEntries(many, [e('new')]);
    expect(b.length).toBe(MAX_ENTRIES);
    expect(b[0]?.cursor).toBe('1');
    expect(b.at(-1)?.cursor).toBe('new');
  });
  it('filters by text and severity', () => {
    expect(matches(e('1', 3, 'Disk FULL'), 'full', false)).toBe(true);
    expect(matches(e('1', 6, 'x', 'CUPSD'), 'cups', false)).toBe(true);
    expect(matches(e('1', 6), '', true)).toBe(false);
    expect(matches(e('1', 4), '', true)).toBe(true);
  });
  it('formats times', () => {
    const now = new Date(2026, 9, 26, 15, 0, 0);
    expect(entryTime(new Date(2026, 9, 26, 14, 3, 7).getTime(), now)).toBe('14:03:07');
    expect(entryTime(new Date(2026, 9, 24, 9, 0, 0).getTime(), now)).toBe('Oct 24 09:00:00');
  });
  it('builds the query', () => {
    expect(journalQuery('system/cups.service', 300, '')).toBe('unit=system%2Fcups.service&lines=300');
    expect(journalQuery('user/a.service', 5, 's=1;i=2')).toBe('unit=user%2Fa.service&lines=5&after=s%3D1%3Bi%3D2');
  });
});

describe('journal page', () => {
  const f: JournalFilter = { text: '', unit: '', scope: 'system', priority: '', hours: 0, boot: '' };
  it('asks only for what is set', () => {
    expect(journalPageQuery(f, 0)).toBe('lines=300');
  });
  it('builds a full query', () => {
    const q = new URLSearchParams(journalPageQuery({ text: ' a b ', unit: 'x.service', scope: 'user', priority: '3', hours: 1, boot: 'c' }, 7_200_000, 's=1'));
    expect(Object.fromEntries(q)).toEqual({ lines: '300', text: 'a b', unit: 'user/x.service', priority: '3', since: '3600', boot: 'c', after: 's=1' });
  });
  it('colours by severity', () => {
    expect([0, 3, 4, 6].map(prioClass)).toEqual(['crit', 'crit', 'warn', '']);
  });
});

import { describe, expect, it } from 'vitest';
import { MAX_SELECTED, freeable, journalFreed, pathParams, toggled, type Report } from './space';

const group = (size: number) => ({ size, count: 1 });
const report: Report = {
  apt: group(100),
  journal: group(3 << 20),
  cache: group(10),
  trash: group(1),
  kernels: [{ package: 'a', kernel: 'k', size: 5 }],
  running: 'x',
  can_trash: true,
  journal_keep: [1],
};

describe('space', () => {
  it('counts what a journal keeps', () => {
    expect(journalFreed(3 << 20, 1)).toBe(2 << 20);
    expect(journalFreed(1 << 20, 1)).toBe(0);
    expect(journalFreed(0, 100)).toBe(0);
  });
  it('adds up what could be freed', () => {
    expect(freeable(report, 1, 1000)).toBe(100 + (2 << 20) + 5 + 10 + 1 + 1000);
  });
  it('sends paths as params in order', () => {
    expect(pathParams(['/a', '/b'])).toEqual({ p00: '/a', p01: '/b' });
  });
  it('selects no more than a confirmation takes', () => {
    let list: string[] = [];
    for (let i = 0; i < MAX_SELECTED + 3; i++) list = toggled(list, String(i));
    expect(list).toHaveLength(MAX_SELECTED);
    expect(toggled(list, '0')).toHaveLength(MAX_SELECTED - 1);
  });
});

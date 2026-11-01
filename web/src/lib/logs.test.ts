import { describe, expect, it } from 'vitest';
import { added, logsQuery, sourceName } from './logs';

describe('logs', () => {
  it('builds the query', () => {
    expect(logsQuery(['container:web', 'system/a.service'], 'bo+m')).toBe('s=container%3Aweb&s=system%2Fa.service&re=bo%2Bm');
    expect(logsQuery(['container:web'], '')).toBe('s=container%3Aweb');
  });
  it('names sources', () => {
    expect(sourceName('container:web')).toBe('web');
    expect(sourceName('user/a.service')).toBe('user/a.service');
  });
  it('keeps at most four distinct sources', () => {
    expect(added(['a'], 'a')).toEqual(['a']);
    expect(added(['a'], '')).toEqual(['a']);
    expect(added(['a', 'b', 'c', 'd'], 'e')).toEqual(['a', 'b', 'c', 'd']);
    expect(added(['a'], 'b')).toEqual(['a', 'b']);
  });
});

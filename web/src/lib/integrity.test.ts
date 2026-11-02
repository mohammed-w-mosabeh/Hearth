import { describe, expect, it } from 'vitest';
import { acceptBody, diffUrl } from './integrity';

describe('integrity', () => {
  it('accepts only what was shown', () => {
    const body = acceptBody([{ path: '~/.bashrc', root: '~/.bashrc', kind: 'changed', hash: 'ab' }]);
    expect(JSON.parse(body)).toEqual({ items: [{ path: '~/.bashrc', hash: 'ab' }] });
  });

  it('escapes the path', () => {
    expect(diffUrl('~/.local/bin/a&b c')).toBe('/security/integrity/diff?path=~%2F.local%2Fbin%2Fa%26b%20c');
  });
});

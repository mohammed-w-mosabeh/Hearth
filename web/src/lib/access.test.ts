import { describe, expect, it } from 'vitest';
import { sessionPlace, type AccessSession } from './access';

const base: AccessSession = { id: '1', user: 'me', uid: 1000, remote: false };

describe('sessionPlace', () => {
  it('prefers the remote host of a remote session', () => {
    expect(sessionPlace({ ...base, remote: true, remoteHost: '192.0.2.1', tty: 'pts/0' })).toBe('192.0.2.1');
    expect(sessionPlace({ ...base, remote: true, tty: 'pts/0' })).toBe('pts/0');
    expect(sessionPlace({ ...base, remote: true })).toBe('?');
  });
  it('names a local terminal or seat, else nothing', () => {
    expect(sessionPlace({ ...base, tty: 'tty2', seat: 'seat0' })).toBe('tty2');
    expect(sessionPlace({ ...base, seat: 'seat0' })).toBe('seat0');
    expect(sessionPlace(base)).toBeNull();
  });
});

import { describe, expect, it } from 'vitest';
import { clearKey, loadKey, type Env } from './session';

const KEY = 'A'.repeat(42) + 'w';

function env(hash: string, stored: string | null = null) {
  const data = new Map<string, string>();
  if (stored !== null) data.set('hearth.session', stored);
  const replaced: string[] = [];
  const e: Env = {
    location: { hash, pathname: '/', search: '' },
    history: { replaceState: (_d, _u, url) => void replaced.push(String(url)) },
    storage: {
      getItem: (k) => data.get(k) ?? null,
      setItem: (k, v) => void data.set(k, v),
      removeItem: (k) => void data.delete(k),
    },
  };
  return { e, data, replaced };
}

describe('loadKey', () => {
  it('moves a valid key from the fragment into storage and strips the URL', () => {
    const { e, data, replaced } = env('#k=' + KEY);
    expect(loadKey(e)).toBe(KEY);
    expect(data.get('hearth.session')).toBe(KEY);
    expect(replaced).toEqual(['/']);
  });

  it('strips but rejects a malformed key', () => {
    for (const bad of ['short', KEY + 'x', KEY.slice(1) + '/', '<img src=x>']) {
      const { e, data, replaced } = env('#k=' + bad);
      expect(loadKey(e)).toBeNull();
      expect(data.size).toBe(0);
      expect(replaced).toEqual(['/']);
    }
  });

  it('falls back to a stored key, validating it', () => {
    expect(loadKey(env('', KEY).e)).toBe(KEY);
    expect(loadKey(env('#/docker', KEY).e)).toBe(KEY);
    expect(loadKey(env('', 'tampered').e)).toBeNull();
    expect(loadKey(env('').e)).toBeNull();
  });

  it('survives storage that throws', () => {
    const { e } = env('#k=' + KEY);
    e.storage = {
      getItem: () => {
        throw new Error('denied');
      },
      setItem: () => {
        throw new Error('denied');
      },
      removeItem: () => {
        throw new Error('denied');
      },
    };
    expect(loadKey(e)).toBe(KEY);
    expect(() => clearKey(e)).not.toThrow();
  });
});

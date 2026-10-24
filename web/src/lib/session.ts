// Session key handling (SECURITY S2). /auth redirects to /#k=<key>; we move
// the key into localStorage (isolated per port, unlike cookies) and strip it
// from the URL so it never lands in history or screenshots.

const STORAGE_KEY = 'hearth.session';
const KEY_RE = /^[A-Za-z0-9_-]{43}$/; // 32 bytes, unpadded base64url

export interface Env {
  location: Pick<Location, 'hash' | 'pathname' | 'search'>;
  history: Pick<History, 'replaceState'>;
  storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
}

/** Takes a key from the URL fragment if present, otherwise the stored one. */
export function loadKey(env: Env): string | null {
  const hash = env.location.hash;
  if (hash.startsWith('#k=')) {
    const key = hash.slice(3);
    env.history.replaceState(null, '', env.location.pathname + env.location.search);
    if (KEY_RE.test(key)) {
      safe(() => env.storage.setItem(STORAGE_KEY, key));
      return key;
    }
  }
  const stored = safe(() => env.storage.getItem(STORAGE_KEY)) ?? null;
  return stored !== null && KEY_RE.test(stored) ? stored : null;
}

export function clearKey(env: Pick<Env, 'storage'>): void {
  safe(() => env.storage.removeItem(STORAGE_KEY));
}

// Storage can throw (disabled, private mode); never let that break the UI.
function safe<T>(fn: () => T): T | undefined {
  try {
    return fn();
  } catch {
    return undefined;
  }
}

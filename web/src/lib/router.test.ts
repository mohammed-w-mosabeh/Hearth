import { describe, expect, it, vi } from 'vitest';

// The router reads location and listens on window at import time.
vi.stubGlobal('location', { hash: '' });
vi.stubGlobal('window', { addEventListener: () => {} });
const { routeId } = await import('./router.svelte');

describe('router', () => {
  it('maps hashes to route ids', () => {
    expect(routeId('')).toBe('home');
    expect(routeId('#/')).toBe('home');
    expect(routeId('#/docker')).toBe('docker');
    expect(routeId('#k=AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcde')).toBe('home');
  });
});

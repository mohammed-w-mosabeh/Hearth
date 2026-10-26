import { describe, expect, it, vi } from 'vitest';

// The router reads location and listens on window at import time.
vi.stubGlobal('location', { hash: '' });
vi.stubGlobal('window', { addEventListener: () => {} });
const { href, routeId, routeQuery } = await import('./router.svelte');

describe('router', () => {
  it('maps hashes to route ids', () => {
    expect(routeId('')).toBe('home');
    expect(routeId('#/')).toBe('home');
    expect(routeId('#/docker')).toBe('docker');
    expect(routeId('#k=AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcde')).toBe('home');
    expect(routeId('#/services?q=cups')).toBe('services');
  });
  it('carries a search', () => {
    expect(routeQuery('#/services?q=cups.service')).toBe('cups.service');
    expect(routeQuery('#/services')).toBe('');
    expect(href('services', 'getty@tty1.service')).toBe('#/services?q=getty%40tty1.service');
    expect(routeQuery(href('services', 'a b&c'))).toBe('a b&c');
    expect(href('home')).toBe('#/');
    expect(routeQuery('#/services?x=1&q=a+b')).toBe('a b');
    expect(routeQuery('#/services?q=%E0%A4')).toBe('');
  });
});

import { describe, expect, it, vi } from 'vitest';
import { emberRequested, loadClosedGroups, loadRailCollapsed, railCurrent, railGroups, saveClosedGroups, saveRailCollapsed } from './ember';

// router.svelte.ts reads the page's location when it loads.
vi.stubGlobal('location', { hash: '' });
vi.stubGlobal('window', { addEventListener: () => {} });
const { pageOf, routes } = await import('./router.svelte');

describe('ember prototype', () => {
  it('turns on only for ?ui=ember', () => {
    expect(emberRequested('?ui=ember')).toBe(true);
    expect(emberRequested('?x=1&ui=ember')).toBe(true);
    expect(emberRequested('')).toBe(false);
    expect(emberRequested('?ui=old')).toBe(false);
  });

  it('keeps every page in the rail, once', () => {
    const ids = railGroups.flatMap((g) => g.items.map((i) => i.id));
    expect(new Set(ids).size).toBe(ids.length);
    const pages = new Set(ids.map(pageOf));
    expect([...pages].sort()).toEqual(routes.map((r) => r.id).sort());
  });

  it('opens the existing pages for Apps and Modes', () => {
    expect(pageOf('stacks')).toBe('docker');
    expect(pageOf('modes')).toBe('profiles');
    expect(pageOf('apps')).toBe('apps');
    expect(railCurrent('modes', 'profiles')).toBe(true);
    expect(railCurrent('docker', 'stacks')).toBe(false);
  });

  it('remembers folded groups and survives damaged storage', () => {
    const store = new Map<string, string>();
    const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) };
    expect(loadClosedGroups(storage)).toEqual([]);
    saveClosedGroups(['nav.groupSystem'], storage);
    expect(loadClosedGroups(storage)).toEqual(['nav.groupSystem']);
    expect(loadClosedGroups({ getItem: () => '{not json' })).toEqual([]);
    expect(loadClosedGroups({ getItem: () => '[1,"x"]' })).toEqual(['x']);
  });

  it('remembers a collapsed rail and survives blocked storage', () => {
    const store = new Map<string, string>();
    const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) };
    expect(loadRailCollapsed(storage)).toBe(false);
    saveRailCollapsed(true, storage);
    expect(loadRailCollapsed(storage)).toBe(true);
    const blocked = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    };
    expect(loadRailCollapsed(blocked)).toBe(false);
    expect(() => saveRailCollapsed(true, blocked)).not.toThrow();
  });
});

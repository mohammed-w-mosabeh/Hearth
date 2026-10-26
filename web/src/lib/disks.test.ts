import { describe, expect, it } from 'vitest';
import { child, crumbs, exploreRoot, level, mountOf, usageQuery } from './disks';

describe('disks', () => {
  it('builds breadcrumbs', () => {
    expect(crumbs('/home/me', '/home/me')).toEqual([{ name: '/home/me', path: '/home/me' }]);
    expect(crumbs('/home/me/Videos/2024', '/home/me')).toEqual([
      { name: '/home/me', path: '/home/me' },
      { name: 'Videos', path: '/home/me/Videos' },
      { name: '2024', path: '/home/me/Videos/2024' },
    ]);
    expect(crumbs('/media/me/disk/x', '/media/me/disk').map((c) => c.path)).toEqual(['/media/me/disk', '/media/me/disk/x']);
    expect(crumbs('/elsewhere', '/home/me')).toHaveLength(1);
    expect(child('/home/me', 'a b')).toBe('/home/me/a b');
  });
  it('maps mounts to explorable roots', () => {
    const roots = ['/home/me', '/media/me/disk'];
    const mounts = ['/', '/home', '/boot/efi', '/media/me/disk', '/media/me/other'];
    expect(exploreRoot('/home', roots, mounts)).toBe('/home/me');
    expect(exploreRoot('/', roots, mounts)).toBeNull(); // the home is on /home
    expect(exploreRoot('/', ['/home/me'], ['/'])).toBe('/home/me');
    expect(exploreRoot('/media/me/disk', roots, mounts)).toBe('/media/me/disk');
    expect(exploreRoot('/media/me/other', roots, mounts)).toBeNull();
    expect(exploreRoot('/boot/efi', roots, mounts)).toBeNull();
    expect(mountOf('/home/me', ['/', '/home', '/boot/efi'])).toBe('/home');
    expect(mountOf('/home2/x', ['/', '/home'])).toBe('/');
  });
  it('grades usage', () => {
    expect(level(50)).toBe('ok');
    expect(level(85)).toBe('warn');
    expect(level(95)).toBe('crit');
    expect(usageQuery('/home/me/a b', true)).toBe('path=%2Fhome%2Fme%2Fa+b&fresh=1');
  });
});

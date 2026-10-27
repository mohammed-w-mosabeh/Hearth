import { describe, expect, it } from 'vitest';
import {
  counts,
  debRows,
  extraRows,
  pkgReasonKey,
  removeKind,
  updateCounts,
  view,
  type DebPackage,
  type Filters,
  type Update,
} from './packages';

const deb = (name: string, extra: Partial<DebPackage> = {}): DebPackage => ({
  name, arch: 'amd64', version: '1', state: 'installed', ...extra,
});

const pkgs = [
  deb('bash', { essential: true, priority: 'required', installed_kib: 2000 }),
  deb('vim', { summary: 'Vi IMproved', installed_kib: 4000 }),
  deb('libc6', { auto: true, protected: true }),
  deb('libc6', { arch: 'i386', auto: true }),
  deb('cowsay', { hold: true, arch: 'all' }),
  deb('old', { state: 'config-files' }),
  deb('hearth'),
];
const updates: Update[] = [
  { name: 'vim', arch: 'amd64', version: '2', severity: 'security', security: true },
  { name: 'cowsay', arch: 'all', version: '4', severity: 'blocked', security: false },
  { name: 'libc6', arch: 'amd64', version: '3', severity: 'normal', security: false },
];
const all: Filters = { manager: 'all', show: 'all', search: '' };

describe('package rows', () => {
  const rows = debRows(pkgs, updates);
  const byKey = new Map(rows.map((r) => [r.key, r]));
  it('keeps installed packages and marks what cannot be removed', () => {
    expect(rows.map((r) => r.key)).not.toContain('apt/old:amd64');
    expect(byKey.get('apt/bash:amd64')).toMatchObject({ removable: false, why: 'essential', size: 2000 * 1024 });
    expect(byKey.get('apt/libc6:amd64')).toMatchObject({ removable: false, why: 'protected', auto: true });
    expect(byKey.get('apt/hearth:amd64')).toMatchObject({ removable: false, why: 'self' });
    expect(byKey.get('apt/vim:amd64')).toMatchObject({ removable: true, id: 'vim' });
    expect(byKey.get('apt/vim:amd64')?.update?.security).toBe(true);
    expect(byKey.get('apt/cowsay:all')).toMatchObject({ held: true });
  });
  it('names the architecture only when two are installed', () => {
    expect(byKey.get('apt/libc6:i386')?.id).toBe('libc6:i386');
    expect(byKey.get('apt/libc6:amd64')?.id).toBe('libc6:amd64');
    expect(byKey.get('apt/libc6:i386')?.update).toBeUndefined();
  });
  it('counts updates without held ones', () => {
    expect(updateCounts(updates)).toEqual({ all: 2, security: 1 });
  });
});

describe('extra rows and view', () => {
  const flat = extraRows('flatpak', {
    manager: 'flatpak', available: true,
    packages: [
      { id: 'user/app/org.x.Vim/x86_64/stable', name: 'org.x.Vim', summary: 'Vim', removable: true, size_bytes: 10 },
      { id: 'system/runtime/org.gnome.Platform/x86_64/47', name: 'org.gnome.Platform', removable: false, why: 'in-use', used_by: ['org.x.Vim'] },
    ],
  });
  const rows = [...debRows(pkgs, updates), ...flat];
  const names = (f: Partial<Filters>) => view(rows, { ...all, ...f }, 'name', false).map((r) => r.key);

  it('keeps the listing fields', () => {
    expect(flat[1]).toMatchObject({ manager: 'flatpak', removable: false, why: 'in-use', usedBy: ['org.x.Vim'], size: -1 });
    expect(counts(rows)).toEqual({ apt: 6, flatpak: 2 });
  });
  it('filters by manager, updates, manual, held and words', () => {
    expect(names({ manager: 'flatpak' })).toHaveLength(2);
    expect(names({ show: 'updates' })).toEqual(['apt/cowsay:all', 'apt/libc6:amd64', 'apt/vim:amd64']);
    expect(names({ show: 'manual' })).not.toContain('apt/libc6:amd64');
    expect(names({ show: 'held' })).toEqual(['apt/cowsay:all']);
    expect(names({ search: 'vim' })).toEqual(['flatpak/user/app/org.x.Vim/x86_64/stable', 'apt/vim:amd64']);
  });
  it('sorts by size and manager', () => {
    expect(view(rows, all, 'size', true)[0]?.name).toBe('vim');
    expect(view(rows, all, 'manager', true)[0]?.manager).toBe('flatpak');
  });
});

describe('actions and reasons', () => {
  it('maps managers to action kinds', () => {
    expect(removeKind('apt')).toBe('package');
    expect(removeKind('go')).toBe('gobin');
    expect(removeKind('flatpak')).toBe('flatpak');
    expect(removeKind('pip')).toBeNull();
  });
  it('words reasons for packages', () => {
    expect(pkgReasonKey('in-use')).toBe('pkg.reason.inUse');
    expect(pkgReasonKey('not-found')).toBe('pkg.reason.notFound');
    expect(pkgReasonKey('essential')).toBe('reason.essential');
    expect(pkgReasonKey('whatever')).toBe('reason.error');
  });
});

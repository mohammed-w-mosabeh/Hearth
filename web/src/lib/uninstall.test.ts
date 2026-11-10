import { describe, expect, it } from 'vitest';
import { DESKTOP_WORDS, defaults, freed, grouped, itemFor, keyOf, needed, params, typedOk, uninstallHref, type UninstallPlan } from './uninstall';

const plan: UninstallPlan = {
  item: 'package:vlc',
  name: 'vlc',
  owner: { manager: 'apt', target: 'vlc', name: 'vlc' },
  freed: 0,
  candidates: [
    { group: 'program', kind: 'package', id: 'vlc:amd64', name: 'vlc', size: 100, preselect: true, reason: '' },
    { group: 'program', kind: 'package', id: 'vlc-plugin:amd64', name: 'vlc-plugin', size: 10, preselect: false, reason: '' },
    { group: 'orphans', kind: 'package', id: 'libvlc5:amd64', name: 'libvlc5', size: 50, preselect: true, reason: '' },
    { group: 'purge', kind: 'package', id: 'vlc:amd64', name: 'vlc', size: 999, preselect: true, reason: '' },
    { group: 'user', kind: 'trash', id: '/h/.config/vlc', name: '/h/.config/vlc', size: 5, preselect: false, reason: 'named like' },
    { group: 'system', kind: 'archive', id: '/var/lib/mysql', name: '/var/lib/mysql', size: 7, preselect: false, reason: '', warn: 'data' },
    { group: 'remains', kind: 'nothing', id: 'libmine:amd64', name: 'libmine', preselect: true, reason: 'installed by you' },
  ],
};

describe('uninstall', () => {
  it('ticks preselected rows and the program, never remains', () => {
    const sel = defaults(plan);
    expect([...sel].sort()).toEqual(['orphans|libvlc5:amd64', 'program|vlc-plugin:amd64', 'program|vlc:amd64', 'purge|vlc:amd64']);
    expect(freed(plan, sel)).toBe(160); // the purge row counts nothing
  });

  it('groups rows in order', () => {
    expect(grouped(plan).map(([g]) => g)).toEqual(['program', 'orphans', 'purge', 'user', 'system', 'remains']);
  });

  it('asks for data paths and the desktop words', () => {
    const sel = new Set([...defaults(plan), 'system|/var/lib/mysql']);
    expect(needed(plan, sel)).toEqual({ desktop: false, data: ['/var/lib/mysql'] });
    expect(typedOk(plan, sel, '', {})).toBe(false);
    expect(typedOk(plan, sel, '', { '/var/lib/mysql': '/var/lib/mysq' })).toBe(false);
    expect(typedOk(plan, sel, '', { '/var/lib/mysql': '/var/lib/mysql' })).toBe(true);
    const desk = { ...plan, desktop: ['gnome-shell:amd64'] };
    expect(typedOk(desk, defaults(desk), 'remove my desk', {})).toBe(false);
    expect(typedOk(desk, defaults(desk), DESKTOP_WORDS, {})).toBe(true);
  });

  it('sends only the plan’s selectable rows', () => {
    const sel = new Set([...defaults(plan), 'remains|libmine:amd64', 'system|/etc/shadow', 'system|/var/lib/mysql']);
    const p = params(plan, sel, '', { '/var/lib/mysql': '/var/lib/mysql' });
    const keys = JSON.parse(p.select ?? '[]') as string[];
    expect(keys).not.toContain('remains|libmine:amd64');
    expect(keys).not.toContain('system|/etc/shadow');
    expect(keys).toContain('system|/var/lib/mysql');
    expect(JSON.parse(p.data ?? '[]')).toEqual(['/var/lib/mysql']);
    expect(p.desktop).toBeUndefined();
  });

  it('names items for every manager', () => {
    expect(itemFor('apt', 'vlc:amd64')).toBe('package:vlc:amd64');
    expect(itemFor('go', 'x')).toBe('gobin:x');
    expect(itemFor('flatpak', 'system/app/a.b.C/x86_64/stable')).toBe('flatpak:system/app/a.b.C/x86_64/stable');
    expect(itemFor('unknown', 'x')).toBeNull();
    expect(uninstallHref('app:a b&c')).toBe('#/removed?item=app%3Aa%20b%26c');
    const first = plan.candidates[0];
    expect(first && keyOf(first)).toBe('program|vlc:amd64');
  });
});

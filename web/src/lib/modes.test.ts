import { describe, expect, it } from 'vitest';
import type { App } from './apps';
import type { Snapshot } from './docker';
import en from './i18n/en.json';
import {
  KINDS,
  SETTINGS,
  TEMPLATES,
  autoAllowed,
  canStop,
  candidates,
  doKey,
  draftFrom,
  isDo,
  kindKey,
  listed,
  sessionCount,
  settingItems,
  settingKey,
  splitSettings,
  templateKey,
  triggerDetail,
  triggerKey,
  unattended,
  type Sources,
  type Trigger,
} from './modes';
import type { Unit } from './services';

const unit = (scope: Unit['scope'], name: string, critical = false): Unit => ({ name, scope, load: 'loaded', active: 'active', sub: 'running', critical });
const docker = { containers: [{ name: 'web', image: 'nginx' }], projects: [{ name: 'stack' }] } as unknown as Snapshot;
const src: Sources = {
  units: [unit('user', 'b.service'), unit('system', 'a.service'), unit('system', 'dbus.service', true)],
  docker,
  apps: [{ id: 'calc', name: 'Calculator' }] as unknown as App[],
  stacks: ['photos', 'notes'],
  connections: [{ uuid: 'u-1', name: 'Home', type: 'wifi' }],
};
const none: Sources = { units: [], docker: null, apps: [], stacks: [], connections: [] };

describe('modes', () => {
  it('names every kind, setting, template and trigger it builds a key for', () => {
    const keys: string[] = [doKey('start'), doKey('stop'), doKey('start', 'connection'), doKey('stop', 'connection')];
    for (const k of [...KINDS, 'stack', 'setting'] as const) keys.push(kindKey(k));
    for (const s of SETTINGS) keys.push(settingKey(s));
    for (const t of TEMPLATES) keys.push(templateKey(t));
    for (const kind of ['schedule', 'wifi', 'battery', 'game'] as const) keys.push(triggerKey({ kind }));
    keys.push(triggerKey({ kind: 'display', event: 'connect' }), triggerKey({ kind: 'display', event: 'disconnect' }));
    for (const r of ['schedule', 'wifi', 'battery', 'display', 'game']) keys.push(`modes.reason.${r}`);
    for (const k of keys) expect(en).toHaveProperty([k]);
    expect(KINDS).not.toContain('setting');
    expect(KINDS).not.toContain('stack'); // managed apps have their own section
  });
  it('shows a setting value as it is', () => {
    expect(isDo('start') && isDo('stop')).toBe(true);
    expect(isDo('70') || isDo('on')).toBe(false);
  });
  it('turns on game mode only for the Gaming template', () => {
    expect(draftFrom('gaming', 'x').auto_game).toBe(true);
    expect(draftFrom('dev', 'x').auto_game).toBe(false);
  });
  it('knows what can run unattended, and what a game mode may hold', () => {
    expect(unattended({ kind: 'unit', target: 'user/a.service' })).toBe(true);
    expect(unattended({ kind: 'unit', target: 'system/a.service' })).toBe(false);
    expect(unattended({ kind: 'container', target: 'web' })).toBe(false);
    expect(unattended({ kind: 'project', target: 'stack' })).toBe(false);
    expect(unattended({ kind: 'app', target: 'calc' })).toBe(true);
    expect(unattended({ kind: 'setting', target: 'dnd' }) && unattended({ kind: 'connection', target: 'x' })).toBe(true);
    // A managed app may prompt, so it waits for approval or a grant (10.7, 10.8).
    expect(unattended({ kind: 'stack', target: 'photos' })).toBe(false);
    expect(autoAllowed({ kind: 'stack', target: 'photos' })).toBe(true);
    expect(autoAllowed({ kind: 'container', target: 'web' })).toBe(false);
  });
  it('never offers to close an app', () => {
    expect(canStop('app')).toBe(false);
    expect(canStop('unit')).toBe(true);
  });
  it('offers what can be picked', () => {
    expect(candidates('unit', src, false).map((c) => c.value)).toEqual(['system/a.service', 'user/b.service']);
    expect(candidates('unit', src, true).map((c) => c.value)).toEqual(['user/b.service']);
    expect(candidates('container', src, false)).toEqual([{ value: 'web', label: 'nginx' }]);
    expect(candidates('container', src, true)).toEqual([]);
    expect(candidates('project', none, false)).toEqual([]);
    expect(candidates('app', src, true)).toEqual([{ value: 'calc', label: 'Calculator' }]);
    expect(candidates('stack', src, true).map((c) => c.value)).toEqual(['notes', 'photos']);
    expect(candidates('connection', src, false)).toEqual([{ value: 'u-1', label: 'Home (wifi)' }]);
    expect(candidates('setting', src, false)).toEqual([]);
  });
  it('finds a listed item whatever is done to it', () => {
    expect(listed([{ kind: 'app', target: 'a', do: 'start' }], { kind: 'app', target: 'a' })).toBe(true);
    expect(listed([{ kind: 'app', target: 'a', do: 'start' }], { kind: 'unit', target: 'a' })).toBe(false);
  });
  it('keeps settings apart from the other items, and drops an empty one', () => {
    const items = [
      { kind: 'setting' as const, target: 'dnd', do: 'on' },
      { kind: 'stack' as const, target: 'photos', do: 'stop' },
      { kind: 'setting' as const, target: 'nonsense', do: 'x' },
    ];
    const { settings, rest } = splitSettings(items);
    expect(settings).toEqual({ dnd: 'on' });
    expect(rest.map((i) => i.target)).toEqual(['photos', 'nonsense']);
    expect(settingItems({ wallpaper: ' ', brightness: '70', dnd: 'on', 'power-profile': 'balanced' })).toEqual([
      { kind: 'setting', target: 'power-profile', do: 'balanced' },
      { kind: 'setting', target: 'brightness', do: '70' },
      { kind: 'setting', target: 'dnd', do: 'on' },
    ]);
  });
  it('counts session settings and names triggers', () => {
    expect(sessionCount({})).toBe(0);
    expect(sessionCount({ do_not_disturb: false, brightness: 40, wallpaper: '', connect: ['a'], disconnect: ['b'], open: ['x'] })).toBe(4);
    const wifi: Trigger = { kind: 'wifi', connection: 'u-1' };
    expect(triggerDetail(wifi, src.connections)).toBe('Home');
    expect(triggerDetail({ kind: 'wifi', connection: 'u-9' }, src.connections)).toBe('u-9');
    expect(triggerDetail({ kind: 'schedule', on_calendar: 'Mon *-*-* 09:00' }, [])).toBe('Mon *-*-* 09:00');
    expect(triggerDetail({ kind: 'battery', below: 30 }, [])).toBe('');
  });
});

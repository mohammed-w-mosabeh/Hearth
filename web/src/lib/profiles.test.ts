import { describe, expect, it } from 'vitest';
import type { App } from './apps';
import type { Snapshot } from './docker';
import en from './i18n/en.json';
import { KINDS, TEMPLATES, canStop, candidates, doKey, draftFrom, kindKey, listed, templateKey, unattended } from './profiles';
import type { Unit } from './services';

const unit = (scope: Unit['scope'], name: string, critical = false): Unit => ({ name, scope, load: 'loaded', active: 'active', sub: 'running', critical });
const units = [unit('user', 'b.service'), unit('system', 'a.service'), unit('system', 'dbus.service', true)];
const docker = { containers: [{ name: 'web', image: 'nginx' }], projects: [{ name: 'stack' }] } as unknown as Snapshot;
const apps = [{ id: 'calc', name: 'Calculator' }] as unknown as App[];

describe('profiles', () => {
  it('has a message for every key it builds', () => {
    const keys: string[] = [doKey('start'), doKey('stop')];
    for (const k of KINDS) keys.push(kindKey(k));
    for (const t of TEMPLATES) keys.push(templateKey(t));
    for (const k of keys) expect(en).toHaveProperty([k]);
  });
  it('turns on game mode only for the Gaming template', () => {
    expect(draftFrom('gaming', 'x').auto_game).toBe(true);
    expect(draftFrom('dev', 'x').auto_game).toBe(false);
  });
  it('knows what can run unattended', () => {
    expect(unattended({ kind: 'unit', target: 'user/a.service' })).toBe(true);
    expect(unattended({ kind: 'unit', target: 'system/a.service' })).toBe(false);
    expect(unattended({ kind: 'container', target: 'web' })).toBe(false);
    expect(unattended({ kind: 'project', target: 'stack' })).toBe(false);
    expect(unattended({ kind: 'app', target: 'calc' })).toBe(true);
  });
  it('never offers to close an app', () => {
    expect(canStop('app')).toBe(false);
    expect(canStop('unit')).toBe(true);
  });
  it('offers what can be picked', () => {
    expect(candidates('unit', units, null, [], false).map((c) => c.value)).toEqual(['system/a.service', 'user/b.service']);
    expect(candidates('unit', units, null, [], true).map((c) => c.value)).toEqual(['user/b.service']);
    expect(candidates('container', [], docker, [], false)).toEqual([{ value: 'web', label: 'nginx' }]);
    expect(candidates('container', [], docker, [], true)).toEqual([]);
    expect(candidates('project', [], null, [], false)).toEqual([]);
    expect(candidates('app', [], null, apps, true)).toEqual([{ value: 'calc', label: 'Calculator' }]);
  });
  it('finds a listed item whatever is done to it', () => {
    expect(listed([{ kind: 'app', target: 'a', do: 'start' }], { kind: 'app', target: 'a' })).toBe(true);
    expect(listed([{ kind: 'app', target: 'a', do: 'start' }], { kind: 'unit', target: 'a' })).toBe(false);
  });
});

import { describe, expect, it } from 'vitest';
import { available, failedCount, health, unitType, view, type Filters, type Unit } from './services';

const u = (name: string, active: string, extra: Partial<Unit> = {}): Unit => ({
  name, scope: 'system', load: 'loaded', active, sub: active === 'active' ? 'running' : 'dead', ...extra,
});

const units = [
  u('cups.service', 'active', { description: 'CUPS Scheduler' }),
  u('ssh.service', 'inactive', { enabled: 'disabled' }),
  u('broken.service', 'failed'),
  u('apt-daily.timer', 'active'),
  u('cups.socket', 'active'),
  u('home.mount', 'active'),
  u('dev-sda.device', 'active'),
  u('syncnotes.service', 'activating', { scope: 'user' }),
];
const all: Filters = { scope: 'all', type: 'all', state: 'all', search: '' };
const names = (list: Unit[]) => list.map((x) => `${x.scope}/${x.name}`);

describe('services view', () => {
  it('reads type and health', () => {
    expect(unitType('a.b.service')).toBe('service');
    expect(health(units[2] as Unit)).toBe('failed');
    expect(health(units[7] as Unit)).toBe('changing');
    expect(failedCount(units)).toBe(1);
  });
  it('filters by scope, type, state and words', () => {
    expect(names(view(units, {}, { ...all, type: 'service' }, 'name', false))).toEqual([
      'system/broken.service', 'system/cups.service', 'system/ssh.service', 'user/syncnotes.service',
    ]);
    expect(names(view(units, {}, { ...all, type: 'other' }, 'name', false))).toEqual(['system/dev-sda.device']);
    expect(names(view(units, {}, { ...all, scope: 'user' }, 'name', false))).toEqual(['user/syncnotes.service']);
    expect(names(view(units, {}, { ...all, type: 'service', state: 'running' }, 'name', false))).toEqual([
      'system/cups.service', 'user/syncnotes.service',
    ]);
    expect(names(view(units, {}, { ...all, state: 'failed' }, 'name', false))).toEqual(['system/broken.service']);
    expect(names(view(units, {}, { ...all, search: 'cups sched' }, 'name', false))).toEqual(['system/cups.service']);
  });
  it('sorts by state, memory and CPU', () => {
    const svc = { ...all, type: 'service' as const };
    expect(names(view(units, {}, svc, 'state', false))[0]).toBe('system/broken.service');
    const stats = { 'system/ssh.service': { mem: 5, cpu: 0 }, 'system/cups.service': { mem: 9, cpu: 1.5 } };
    expect(names(view(units, stats, svc, 'mem', true)).slice(0, 2)).toEqual(['system/cups.service', 'system/ssh.service']);
    expect(names(view(units, stats, svc, 'cpu', true))[0]).toBe('system/cups.service');
  });
});

describe('available actions', () => {
  it('follows state and unit file state', () => {
    expect(available(u('cups.service', 'active', { enabled: 'enabled' }))).toEqual(['stop', 'restart', 'disable', 'mask', 'override']);
    expect(available(u('ssh.service', 'inactive', { enabled: 'disabled' }))).toEqual(['start', 'enable', 'mask', 'override']);
    expect(available(u('broken.service', 'failed', { enabled: 'static' }))).toEqual(['start', 'mask', 'override']);
    expect(available(u('x.service', 'inactive', { enabled: 'masked' }))).toEqual(['unmask']);
    expect(available(u('hearth-broker.service', 'active', { enabled: 'static' }))).toEqual(['stop', 'restart']);
    expect(available(u('cups.socket', 'inactive', { enabled: 'static' }))).toEqual(['start', 'mask']);
    expect(available(u('dev-sda.device', 'active'))).toEqual([]);
    expect(available(u('session-2.scope', 'active'))).toEqual(['stop']);
  });
});

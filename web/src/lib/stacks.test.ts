import { describe, expect, it } from 'vitest';
import type { Project } from './docker';
import { addresses, backupQuery, detected, dryRunQuery, exposure, logSources, otherFS, presetOf, restartPolicies, serviceCount, shape, storage, type BackupPreview, type StackInfo } from './stacks';

function app(over: Partial<StackInfo> = {}): StackInfo {
  return {
    name: 'photos',
    spec_sha256: 'a'.repeat(64),
    state: 'running',
    spec: {
      name: 'photos',
      services: {
        web: { image: 'x', ports: [{ host_ip: '0.0.0.0', host: 8102, container: 80 }, { host: 9000, container: 90 }], volumes: [{ type: 'bind', source: '/srv/photos', target: '/data' }] },
        db: { image: 'y', volumes: [{ type: 'volume', source: 'db', target: '/var/lib/db' }] },
      },
      volumes: { db: {}, cache: { name: 'shared_cache' } },
      opt_in: ['port:0.0.0.0:8102/tcp', 'device:/dev/dri'],
    },
    containers: [
      { id: '1', name: 'photos-web-1', service: 'web', state: 'running', image: 'x', image_id: 'i', restart: 'unless-stopped', restart_count: 0 },
      { id: '2', name: 'photos-db-1', service: 'db', state: 'running', image: 'y', image_id: 'j', restart: 'unless-stopped', restart_count: 2 },
    ],
    secrets: [],
    ...over,
  };
}

describe('stacks', () => {
  it('shapes an app by its state and health', () => {
    expect(shape(app())).toBe('ok');
    expect(shape(app({ state: 'partial' }))).toBe('degraded');
    expect(shape(app({ state: 'stopped' }))).toBe('stopped');
    expect(shape(app({ state: 'absent', containers: [] }))).toBe('stopped');
    const sick = app();
    sick.containers = sick.containers.map((c, i) => (i === 0 ? { ...c, health: 'unhealthy' } : c));
    expect(shape(sick)).toBe('warning');
  });

  it('counts running services, not containers', () => {
    expect(serviceCount(app())).toBe('2/2');
    const half = app();
    half.containers = half.containers.map((c, i) => (i === 1 ? { ...c, state: 'exited' } : c));
    expect(serviceCount(half)).toBe('1/2');
  });

  it('lists addresses, 127.0.0.1 by default', () => {
    expect(addresses(app())).toEqual(['0.0.0.0:8102', '127.0.0.1:9000']);
  });

  it('keeps network exposure apart from other danger flags', () => {
    expect(exposure(app())).toEqual({ exposed: ['0.0.0.0:8102/tcp'], danger: ['device:/dev/dri'] });
  });

  it('lists restart policies and storage', () => {
    expect(restartPolicies(app())).toEqual(['unless-stopped']);
    expect(storage(app())).toEqual([
      { kind: 'volume', name: 'photos_db' },
      { kind: 'volume', name: 'shared_cache' },
      { kind: 'bind', name: '/srv/photos' },
    ]);
  });

  it('detects only the projects Hearth does not manage', () => {
    const p = (name: string): Project => ({ name, config_files: [], services: [], containers: [], running: 0 });
    expect(detected([p('zeta'), p('photos'), p('alpha')], [app()]).map((x) => x.name)).toEqual(['alpha', 'zeta']);
  });

  it('gives the log explorer its sources', () => {
    expect(logSources(app())).toEqual(['container:photos-db-1', 'container:photos-web-1']);
  });

  it('builds a dry-run query', () => {
    expect(dryRunQuery([], [])).toBe('');
    expect(dryRunQuery(['TZ', 'discovery.type'], ['tools'])).toBe('?plain=TZ%2Cdiscovery.type&skip=tools');
  });
});

describe('backups', () => {
  it('builds the preview query', () => {
    expect(backupQuery(7, [])).toBe('?keep=7');
    expect(backupQuery(0, [])).toBe('');
    expect(backupQuery(31, ['/mnt/a b', '/x&y'])).toBe('?include=%2Fmnt%2Fa+b&include=%2Fx%26y');
  });
  it('finds the binds on another filesystem', () => {
    const p = { binds: [{ real: '/a', other_fs: true }, { real: '/b' }] } as unknown as BackupPreview;
    expect(otherFS(p).map((b) => b.real)).toEqual(['/a']);
  });
});

describe('backup schedule', () => {
  it('names presets', () => {
    expect(presetOf(null)).toBe('off');
    expect(presetOf({ on_calendar: 'Sun *-*-* 03:00:00', keep: 7, stop_first: true, include: [] })).toBe('weekly');
    expect(presetOf({ on_calendar: 'Mon..Fri 22:00', keep: 7, stop_first: true, include: [] })).toBe('custom');
  });
});

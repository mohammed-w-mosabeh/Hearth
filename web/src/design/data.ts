// Invented data for the fixture screens. No real project, path or secret.
export type Shape = 'ok' | 'degraded' | 'warning' | 'stopped';
export const shapes: Record<Shape, string> = { ok: '●', degraded: '◐', warning: '▲', stopped: '■' };

export interface FixtureApp {
  name: string;
  state: Shape;
  services: string;
  address: string;
  exposed: boolean;
  update: boolean;
  backup: string;
  flags: string[];
}

/** The app the detail, import and backup screens show. */
export const sampleApp: FixtureApp = { name: 'photos', state: 'degraded', services: '3/4', address: '0.0.0.0:8102', exposed: true, update: true, backup: '02:30', flags: ['gpu'] };

export const apps: FixtureApp[] = [
  { name: 'notes', state: 'ok', services: '2/2', address: '127.0.0.1:8101', exposed: false, update: false, backup: '02:00', flags: [] },
  sampleApp,
  { name: 'feeds', state: 'stopped', services: '0/1', address: '127.0.0.1:8103', exposed: false, update: false, backup: '—', flags: [] },
  { name: 'monitor', state: 'warning', services: '1/1', address: '127.0.0.1:8104', exposed: false, update: false, backup: '03:00', flags: ['docker_sock'] },
];

export const detected = [
  { name: 'dev-shop', services: '0/5', reason: 'build' },
  { name: 'sample-api', services: '2/2', reason: 'env_file' },
];

export const secrets = ['DB_PASSWORD', 'SESSION_SECRET', 'DATABASE_URL'];
export const volumes = [
  { name: 'photos_db', size: '1.2 GB', kind: 'volume' },
  { name: 'photos_cache', size: '640 MB', kind: 'volume' },
  { name: '/srv/photos/library', size: '38 GB', kind: 'bind' },
];
export const backups = [
  { time: '2026-11-02 02:30', size: '1.4 GB', ok: true },
  { time: '2026-11-01 02:30', size: '1.4 GB', ok: true },
  { time: '2026-10-31 02:30', size: '—', ok: false },
];
export const modes = [
  { name: 'Work', on: true, session: 3, stacks: 2, trigger: 'schedule' },
  { name: 'Battery', on: false, session: 2, stacks: 3, trigger: 'battery' },
  { name: 'Gaming', on: false, session: 1, stacks: 4, trigger: 'game' },
];

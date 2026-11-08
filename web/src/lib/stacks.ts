// Apps (ROADMAP 9.9): the stacks Hearth manages, as GET /api/v1/stacks
// gives them, and the Compose projects it only detects. Pure helpers; the
// page and its tests share them.

import type { Project } from './docker';

export interface StackContainer {
  id: string;
  name: string;
  service: string;
  state: string;
  health?: string;
  image: string;
  image_id: string;
  restart: string;
  restart_count: number;
  started_at?: string;
}

export interface StackSecret {
  key: string;
  services: string[];
  set: boolean;
}

export interface SpecPort {
  host_ip?: string;
  host: number;
  container: number;
  protocol?: string;
}

export interface SpecMount {
  type: 'volume' | 'bind';
  source: string;
  target: string;
  read_only?: boolean;
}

export interface SpecService {
  image: string;
  ports?: SpecPort[];
  volumes?: SpecMount[];
  restart?: string;
  secrets?: string[];
}

export interface Spec {
  name: string;
  services: Record<string, SpecService>;
  volumes?: Record<string, { name?: string; external?: boolean }>;
  bind_roots?: string[];
  opt_in?: string[];
}

export interface StackInfo {
  name: string;
  spec_sha256: string;
  spec: Spec;
  state: 'running' | 'partial' | 'stopped' | 'absent';
  containers: StackContainer[];
  secrets: StackSecret[];
}

/** The design's state shapes, never colour alone (docs/design/ember.md). */
export type Shape = 'ok' | 'degraded' | 'warning' | 'stopped';
export const shapes: Record<Shape, string> = { ok: '●', degraded: '◐', warning: '▲', stopped: '■' };

/** An app's shape: unhealthy or restarting containers make it degraded. */
export function shape(s: StackInfo): Shape {
  if (s.state === 'stopped' || s.state === 'absent') return 'stopped';
  if (s.containers.some((c) => c.health === 'unhealthy' || c.state === 'restarting')) return 'warning';
  if (s.state === 'partial' || s.containers.some((c) => c.health === 'starting')) return 'degraded';
  return 'ok';
}

/** "running/total" services. */
export function serviceCount(s: StackInfo): string {
  const total = Object.keys(s.spec.services ?? {}).length;
  const running = new Set(s.containers.filter((c) => c.state === 'running').map((c) => c.service)).size;
  return `${running}/${total}`;
}

/** Every published address, sorted: "127.0.0.1:8080". */
export function addresses(s: StackInfo): string[] {
  const out: string[] = [];
  for (const svc of Object.values(s.spec.services ?? {})) {
    for (const p of svc.ports ?? []) out.push(`${p.host_ip || '127.0.0.1'}:${p.host}`);
  }
  return out.sort();
}

/** A spec's danger flags: what it opts in to. */
export function flags(s: StackInfo): string[] {
  return [...(s.spec.opt_in ?? [])].sort();
}

/** Flags that open a port to the network, apart from the rest. */
export function exposure(s: StackInfo): { exposed: string[]; danger: string[] } {
  const exposed: string[] = [];
  const danger: string[] = [];
  for (const f of flags(s)) {
    if (f.startsWith('port:')) exposed.push(f.slice('port:'.length));
    else danger.push(f);
  }
  return { exposed, danger };
}

/** The restart policies its containers run with, deduplicated. */
export function restartPolicies(s: StackInfo): string[] {
  return [...new Set(s.containers.map((c) => c.restart).filter((r) => r !== ''))].sort();
}

/** Its volumes: named ones by Docker name, then bind sources. */
export function storage(s: StackInfo): { kind: 'volume' | 'bind'; name: string }[] {
  const named = Object.entries(s.spec.volumes ?? {}).map(([k, v]) => ({ kind: 'volume' as const, name: v.name || `${s.name}_${k}` }));
  const binds = new Set<string>();
  for (const svc of Object.values(s.spec.services ?? {})) {
    for (const m of svc.volumes ?? []) if (m.type === 'bind') binds.add(m.source);
  }
  return [...named.sort((a, b) => (a.name < b.name ? -1 : 1)), ...[...binds].sort().map((name) => ({ kind: 'bind' as const, name }))];
}

/** Compose projects Hearth does not manage: the Detected group. */
export function detected(projects: Project[], managed: StackInfo[]): Project[] {
  const mine = new Set(managed.map((s) => s.name));
  return projects.filter((p) => !mine.has(p.name)).sort((a, b) => (a.name < b.name ? -1 : 1));
}

/** Container names to show in the log explorer, as its source ids. */
export function logSources(s: StackInfo): string[] {
  return s.containers.map((c) => `container:${c.name}`).sort();
}

// --- the import dry-run (GET /api/v1/stacks/import/{project}) ---

export interface DryRun {
  project: string;
  dir: string;
  files: string[];
  containers: { name: string; service: string; state: string }[];
  spec: Spec;
  secrets: { key: string; services: string[] }[];
  refused: string[];
  changes: string[];
  flags: string[];
  volumes: { key: string; name: string; exists: boolean }[];
  ports: { service: string; host_ip: string; host: number; protocol: string; conflict?: string }[];
  notes: string[];
}

/** The query of a dry-run: names marked plain and services left out. */
export function dryRunQuery(plain: string[], skip: string[]): string {
  const q = new URLSearchParams();
  if (plain.length) q.set('plain', plain.join(','));
  if (skip.length) q.set('skip', skip.join(','));
  const s = q.toString();
  return s ? `?${s}` : '';
}

// --- backups (ROADMAP 9.10) ---

export interface BackupBind {
  service: string;
  source: string;
  real: string;
  size: number;
  partial?: boolean;
  other_fs?: boolean;
  included: boolean;
}

export interface BackupPreview {
  name: string;
  running: boolean;
  volumes: { name: string; size: number; included: boolean; why?: string }[];
  binds: BackupBind[];
  secrets: string[];
  estimate: number;
  partial: boolean;
  free: number;
  enough: boolean;
  keep: number;
  archives: string[];
  prune: string[];
}

export interface BackupEntry {
  archive: string;
  created: string;
  size: number;
  stopped_first: boolean;
  keep: number;
  volumes: { name: string; size: number }[];
  binds: BackupBind[];
  secrets: string[];
  warning?: string;
}

export interface BackupSchedule {
  on_calendar: string;
  keep: number;
  stop_first: boolean;
  include: string[];
}

export interface BackupLast {
  at: string;
  scheduled: boolean;
  ok: boolean;
  archive?: string;
  error?: string;
}

/** GET /app-health: one managed app as Home sees it. */
export interface AppHealth {
  name: string;
  state: string;
  /** Containers unhealthy, restarting, or exited while the app should run. */
  unhealthy: string[];
  backup: BackupLast | null;
  /** Services the owner's last update preview found a new digest for. */
  updates: string[];
  scheduled: boolean;
}

export interface BackupList {
  name: string;
  dir: string;
  archives: BackupEntry[];
  schedule: BackupSchedule | null;
  last: BackupLast | null;
  next: number[];
}

/** Schedule presets, as OnCalendar expressions; anything else is custom. */
export const schedulePresets = {
  daily: '*-*-* 03:00:00',
  weekly: 'Sun *-*-* 03:00:00',
  monthly: '*-*-01 03:00:00',
} as const;
export type SchedulePreset = keyof typeof schedulePresets | 'off' | 'custom';

/** Which preset a saved schedule is. */
export function presetOf(s: BackupSchedule | null): SchedulePreset {
  if (!s || s.on_calendar === '') return 'off';
  for (const [k, v] of Object.entries(schedulePresets)) if (v === s.on_calendar) return k as SchedulePreset;
  return 'custom';
}

/** The query of a backup preview: how many to keep, and the binds on
 *  another filesystem chosen. */
export function backupQuery(keep: number, include: string[]): string {
  const q = new URLSearchParams();
  if (Number.isInteger(keep) && keep >= 1 && keep <= 30) q.set('keep', String(keep));
  for (const p of include) q.append('include', p);
  const s = q.toString();
  return s ? `?${s}` : '';
}

/** Binds on another filesystem than the backups': left out unless chosen. */
export function otherFS(p: BackupPreview): BackupBind[] {
  return p.binds.filter((b) => b.other_fs);
}

/** GET /proxy: app names at http://<app>.localhost:<port>/ (9.13). */
export interface ProxyStatus {
  enabled: boolean;
  port: number;
  /** App name to the port its name opens. */
  routes: Record<string, number>;
  running: boolean;
  error?: string;
  /** Each app's TCP ports published on this computer. */
  ports: Record<string, number[]>;
}

/** The address an app's name opens. */
export function proxyURL(name: string, port: number): string {
  return `http://${name}.localhost:${port}/`;
}

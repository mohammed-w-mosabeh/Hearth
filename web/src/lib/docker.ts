// Docker pages: types of the "docker" snapshot (internal/discovery),
// live logs ("docker.logs:<id>") and stats ("docker.stats", internal/docker),
// and the pure helpers the pages use.

import { ltr } from './i18n';

export type DockerState = 'ok' | 'no-broker' | 'not-member' | 'no-docker' | 'error';

export interface Port {
  ip?: string;
  private: number;
  public?: number;
  type: string;
}

export interface Mount {
  type: string;
  name?: string;
  source?: string;
  destination: string;
  rw: boolean;
}

export interface Container {
  id: string;
  name: string;
  image: string;
  image_id: string;
  command: string;
  created: number;
  state: string;
  status: string;
  health?: string;
  ports: Port[];
  networks: string[];
  mounts: Mount[];
  project?: string;
  service?: string;
}

export interface Image {
  id: string;
  tags: string[];
  digests: string[];
  created: number;
  size: number;
  shared_size: number; // -1 when unknown
  dangling: boolean;
  containers: string[];
}

export interface Network {
  id: string;
  name: string;
  driver: string;
  scope: string;
  internal: boolean;
  created: string;
  project?: string;
  containers: string[];
}

export interface Volume {
  name: string;
  driver: string;
  mountpoint: string;
  created: string;
  project?: string;
  containers: string[];
}

export interface Project {
  name: string;
  working_dir?: string;
  config_files: string[];
  services: string[];
  containers: string[];
  running: number;
}

export interface Snapshot {
  kind: 'snapshot';
  state: DockerState;
  error?: string;
  version?: string;
  time: number;
  containers: Container[];
  images: Image[];
  networks: Network[];
  volumes: Volume[];
  projects: Project[];
}

export interface DockerEvent {
  type: string;
  action: string;
  id: string;
  name?: string;
  image?: string;
  time: number;
}

export type DockerMsg = Snapshot | { kind: 'event'; event: DockerEvent };

export interface LogLine {
  stream: 'stdout' | 'stderr';
  text: string;
}

export type LogsMsg =
  | { kind: 'lines'; reset?: boolean; lines: LogLine[] }
  | { kind: 'end'; reset?: boolean; error?: string };

export interface Point {
  t: number;
  cpu: number;
  mem: number;
  mem_limit: number;
  net_rx: number;
  net_tx: number;
  blk_read: number;
  blk_write: number;
  pids: number;
}

export type StatsMsg =
  | { kind: 'history'; history?: Record<string, Point[]> } // left out when nothing has samples
  | { kind: 'sample'; sample?: Record<string, Point> }; // left out when nothing runs

/** Samples kept per container (internal/docker HistoryLen). */
export const HISTORY_LEN = 150;

/** Seconds of stats history a chart shows (150 samples at 2 s). */
export const STATS_WINDOW_S = 300;

/** Lines kept in a log view. */
export const MAX_LOG_LINES = 5000;

/** A running container's CPU and memory (topic "docker.usage", from cgroups). */
export interface Usage {
  id: string;
  name: string;
  cpu: number; // percent of one CPU
  mem: number; // bytes
}

export interface UsageMsg {
  available: boolean; // Docker is installed
  running: Usage[]; // busiest first
  stopped: number;
  unexpected: string[]; // containers that stopped on their own
  time: number;
}

export type Tone = 'running' | 'paused' | 'changing' | 'failed' | 'stopped';

// Exit codes of a normal stop: done, or ended by a signal from Ctrl+C or
// `docker stop` (SIGINT 130, SIGTERM 143, SIGKILL 137 after the timeout).
const cleanExit = new Set([0, 130, 137, 143]);

/** One word for the dot: health wins over state. */
export function tone(c: Container): Tone {
  if (c.health === 'unhealthy' || c.state === 'dead') return 'failed';
  if (c.state === 'running') return c.health === 'starting' ? 'changing' : 'running';
  if (c.state === 'paused') return 'paused';
  if (c.state === 'restarting' || c.state === 'removing') return 'changing';
  if (c.state === 'exited' && !cleanExit.has(exitCode(c.status) ?? 0)) return 'failed';
  return 'stopped';
}

/** The exit code in a status like "Exited (137) 2 hours ago", or null. */
export function exitCode(status: string): number | null {
  const m = /^Exited \((-?\d+)\)/.exec(status);
  return m ? Number(m[1]) : null;
}

export type ContainerOp = 'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'remove' | 'limit';

/** The actions that make sense for a container in its current state. */
export function available(c: Container): ContainerOp[] {
  switch (c.state) {
    case 'running':
      return ['stop', 'restart', 'pause', 'remove'];
    case 'paused':
      return ['unpause', 'stop', 'remove'];
    case 'restarting':
      return ['stop', 'remove'];
    case 'removing':
      return [];
    default:
      return ['start', 'remove'];
  }
}

/** "127.0.0.1:8080→80/tcp", one per published port; unpublished ones as "80/tcp". */
export function portText(p: Port): string {
  const inner = `${p.private}/${p.type}`;
  if (!p.public) return inner;
  const host = p.ip && p.ip !== '0.0.0.0' && p.ip !== '::' ? `${p.ip.includes(':') ? `[${p.ip}]` : p.ip}:` : '';
  return ltr(`${host}${p.public}→${inner}`);
}

/**
 * Published ports without the IPv4/IPv6 duplicates Docker lists for one
 * binding, published first.
 */
export function ports(c: Container): Port[] {
  const seen = new Set<string>();
  const out: Port[] = [];
  for (const p of c.ports) {
    const wild = !p.ip || p.ip === '0.0.0.0' || p.ip === '::';
    const k = `${wild ? '*' : p.ip}|${p.public ?? ''}|${p.private}|${p.type}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(p);
  }
  return out.sort((a, b) => Number(!a.public) - Number(!b.public) || a.private - b.private);
}

/** A local URL for a port published on all or loopback addresses, else null. */
export function portURL(p: Port): string | null {
  if (!p.public || p.type !== 'tcp') return null;
  if (p.ip && p.ip !== '0.0.0.0' && p.ip !== '::' && p.ip !== '127.0.0.1' && p.ip !== '::1') return null;
  return `http://127.0.0.1:${p.public}/`;
}

/** A short image name: no "sha256:" ID, no digest. */
export function imageName(c: Container): string {
  if (c.image.startsWith('sha256:')) return c.image.slice(7, 19);
  return c.image.replace(/@sha256:[0-9a-f]+$/, '');
}

export type StateFilter = 'all' | 'running' | 'stopped' | 'problem';
export type SortKey = 'name' | 'state' | 'cpu' | 'mem' | 'created';

// States in which a container has processes.
const up = new Set(['running', 'paused', 'restarting']);

const toneOrder: Record<Tone, number> = { failed: 0, changing: 1, running: 2, paused: 3, stopped: 4 };

/** Containers matching the filter and search, sorted; ties by name. */
export function view(
  list: Container[],
  stats: Record<string, Point>,
  state: StateFilter,
  search: string,
  sort: SortKey,
  desc: boolean,
): Container[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const out = list.filter((c) => {
    if (state === 'running' && !up.has(c.state)) return false;
    if (state === 'stopped' && up.has(c.state)) return false;
    if (state === 'problem' && tone(c) !== 'failed') return false;
    if (words.length === 0) return true;
    const hay = `${c.name} ${c.image} ${c.project ?? ''} ${c.service ?? ''} ${c.id.slice(0, 12)}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  const dir = desc ? -1 : 1;
  const value = (c: Container): number | string => {
    switch (sort) {
      case 'name':
        return c.name.toLowerCase();
      case 'state':
        return toneOrder[tone(c)];
      case 'cpu':
        return stats[c.id]?.cpu ?? -1;
      case 'mem':
        return stats[c.id]?.mem ?? -1;
      case 'created':
        return c.created;
    }
  };
  return out.sort((a, b) => {
    const va = value(a);
    const vb = value(b);
    if (va < vb) return -dir;
    if (va > vb) return dir;
    return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
  });
}

/** Containers in trouble, for the banner. */
export function problemCount(list: Container[]): number {
  return list.filter((c) => tone(c) === 'failed').length;
}

/** Appends a logs message to lines, keeping the last MAX_LOG_LINES. */
export function appendLogs(lines: LogLine[], m: LogsMsg): LogLine[] {
  const base = m.reset ? [] : lines;
  if (m.kind !== 'lines') return base;
  const next = base.concat(m.lines);
  return next.length > MAX_LOG_LINES ? next.slice(next.length - MAX_LOG_LINES) : next;
}

/** Lines containing every word of filter (case-insensitive), optionally stderr only. */
export function filterLogs(lines: LogLine[], filter: string, stderrOnly: boolean): LogLine[] {
  const words = filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0 && !stderrOnly) return lines;
  return lines.filter((l) => (!stderrOnly || l.stream === 'stderr') && words.every((w) => l.text.toLowerCase().includes(w)));
}

/** Merges a stats message into the per-container history. */
export function mergeStats(history: Record<string, Point[]>, m: StatsMsg): Record<string, Point[]> {
  if (m.kind === 'history') return { ...m.history };
  const out: Record<string, Point[]> = { ...history };
  for (const [id, p] of Object.entries(m.sample ?? {})) {
    const h = out[id] ?? [];
    if (h.at(-1)?.t === p.t) continue;
    const next = h.concat(p);
    out[id] = next.length > HISTORY_LEN ? next.slice(next.length - HISTORY_LEN) : next;
  }
  return out;
}

/** The last sample of each container. */
export function latest(history: Record<string, Point[]>): Record<string, Point> {
  const out: Record<string, Point> = {};
  for (const [id, h] of Object.entries(history)) {
    const p = h.at(-1);
    if (p) out[id] = p;
  }
  return out;
}

/** Sparkline columns (seconds, then each field) of a container's history. */
export function columns(h: Point[], ...fields: (keyof Omit<Point, 't'>)[]): [number[], ...number[][]] {
  return [h.map((p) => p.t / 1000), ...fields.map((f) => h.map((p) => p[f]))];
}

/** Env entries "KEY=value" split for a table; the broker already masked secrets. */
export function envPairs(env: string[] | null | undefined): [string, string][] {
  return (env ?? []).map((e) => {
    const i = e.indexOf('=');
    return i < 0 ? [e, ''] : [e.slice(0, i), e.slice(i + 1)];
  });
}

/** The WebSocket URL of a container shell (internal/docker HandleExec). */
export function execURL(base: string, id: string, cols: number, rows: number): string {
  const url = new URL(`/api/v1/docker/exec/${encodeURIComponent(id)}`, base);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  url.search = new URLSearchParams({ cols: String(cols), rows: String(rows) }).toString();
  return url.toString();
}

// --- images ---

/** The name an image is shown by: its first tag, else its short ID. */
export function imageTitle(im: Image): string {
  return im.tags[0] ?? shortID(im.id);
}

/** "sha256:0123456789ab…" -> "0123456789ab". */
export function shortID(id: string): string {
  return id.replace(/^sha256:/, '').slice(0, 12);
}

/** Bytes only this image uses (its size minus layers shared with others), or null. */
export function uniqueSize(im: Image): number | null {
  return im.shared_size >= 0 ? Math.max(0, im.size - im.shared_size) : null;
}

export type ImageFilter = 'all' | 'used' | 'unused' | 'dangling';
export type ImageSort = 'name' | 'size' | 'created';

/** Images matching the filter and search, sorted; ties by ID. */
export function imageView(list: Image[], filter: ImageFilter, search: string, sort: ImageSort, desc: boolean): Image[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const out = list.filter((im) => {
    if (filter === 'used' && im.containers.length === 0) return false;
    if (filter === 'unused' && im.containers.length > 0) return false;
    if (filter === 'dangling' && !im.dangling) return false;
    const hay = `${im.tags.join(' ')} ${shortID(im.id)}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  const dir = desc ? -1 : 1;
  const value = (im: Image): number | string =>
    sort === 'size' ? im.size : sort === 'created' ? im.created : imageTitle(im).toLowerCase();
  return out.sort((a, b) => {
    const va = value(a);
    const vb = value(b);
    if (va < vb) return -dir;
    if (va > vb) return dir;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

/** One layer of an image's history (Docker's /images/{id}/history). */
export interface Layer {
  Id: string;
  Created: number;
  CreatedBy: string;
  Size: number;
  Comment?: string;
}

/** A layer's command without the legacy shell wrapper and #(nop) marker. */
export function layerCommand(createdBy: string): string {
  return createdBy
    .replace(/^\/bin\/sh -c #\(nop\)\s*/, '')
    .replace(/^\/bin\/sh -c /, 'RUN ')
    .replace(/\s+/g, ' ')
    .trim();
}

export interface TagUpdate {
  tag: string;
  status: 'current' | 'update' | 'local' | 'unknown';
  local?: string;
  remote?: string;
  error?: string;
}

export interface Vuln {
  id: string;
  package: string;
  installed: string;
  fixed?: string;
  severity: 'CRITICAL' | 'HIGH' | 'MEDIUM' | 'LOW' | 'UNKNOWN';
}

export interface Scan {
  counts: Partial<Record<Vuln['severity'], number>>;
  vulns: Vuln[];
  truncated: boolean;
}

export const SEVERITIES: Vuln['severity'][] = ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'UNKNOWN'];

/** A link to a vulnerability's public record, for known ID schemes; else null. */
export function vulnURL(id: string): string | null {
  if (/^CVE-\d{4}-\d{4,}$/.test(id)) return `https://nvd.nist.gov/vuln/detail/${id}`;
  if (/^GHSA(-[23456789cfghjmpqrvwx]{4}){3}$/.test(id)) return `https://github.com/advisories/${id}`;
  return null;
}

// --- compose projects ---

export type ProjectState = 'running' | 'partial' | 'stopped';

/**
 * Containers of a project that ran to completion (exit 0), like
 * one-shot init services; they do not make a running project partial.
 */
export function finished(p: Project, byId: ReadonlyMap<string, Container>): number {
  return p.containers.filter((id) => {
    const c = byId.get(id);
    return c?.state === 'exited' && exitCode(c.status) === 0;
  }).length;
}

export function projectState(p: Project, byId: ReadonlyMap<string, Container> = new Map()): ProjectState {
  if (p.running === 0) return 'stopped';
  return p.running + finished(p, byId) >= p.containers.length ? 'running' : 'partial';
}

export type ProjectOp = 'start' | 'stop' | 'restart';

/** The group actions that make sense for a project now. */
export function projectOps(p: Project, byId: ReadonlyMap<string, Container> = new Map()): ProjectOp[] {
  const st = projectState(p, byId);
  if (st === 'stopped') return ['start'];
  if (st === 'running') return ['stop', 'restart'];
  return ['start', 'stop', 'restart'];
}

/** Projects matching search (name, service or directory), running ones first, then by name. */
export function projectView(list: Project[], search: string, byId: ReadonlyMap<string, Container> = new Map()): Project[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const order: Record<ProjectState, number> = { partial: 0, running: 1, stopped: 2 };
  return list
    .filter((p) => {
      const hay = `${p.name} ${p.services.join(' ')} ${p.working_dir ?? ''}`.toLowerCase();
      return words.every((w) => hay.includes(w));
    })
    .sort((a, b) => order[projectState(a, byId)] - order[projectState(b, byId)] || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

// --- cleanup ---

export type PruneKind = 'containers' | 'images' | 'networks' | 'volumes' | 'build';

export interface PruneItem {
  id: string;
  name?: string;
  size: number; // -1 when Docker does not know
}

export interface KindSummary {
  kind: PruneKind;
  count: number;
  reclaimable: number;
  items: PruneItem[];
  error?: string;
}

/** Bytes every kind together would free (kinds that failed count as 0). */
export function pruneTotal(kinds: KindSummary[]): number {
  return kinds.reduce((n, k) => n + (k.error ? 0 : Math.max(0, k.reclaimable)), 0);
}

/** Docker's own networks, which cannot be removed. */
export const BUILT_IN_NETWORKS = ['bridge', 'host', 'none'];

/** Container names for IDs; an ID no longer listed shows its first 12 digits. */
export function usedByNames(ids: string[], containers: Pick<Container, 'id' | 'name'>[]): string[] {
  return ids.map((id) => containers.find((c) => c.id === id)?.name ?? id.slice(0, 12));
}

/** A network the page offers to remove. The server checks again. */
export function networkRemovable(n: Pick<Network, 'name' | 'containers'>): boolean {
  return n.containers.length === 0 && !BUILT_IN_NETWORKS.includes(n.name);
}

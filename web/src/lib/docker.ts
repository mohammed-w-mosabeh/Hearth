// Docker pages: types of the "docker" snapshot (internal/discovery),
// live logs ("docker.logs:<id>") and stats ("docker.stats", internal/docker),
// and the pure helpers the pages use.

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
  | { kind: 'history'; history: Record<string, Point[]> }
  | { kind: 'sample'; sample: Record<string, Point> };

/** Samples kept per container (internal/docker HistoryLen). */
export const HISTORY_LEN = 150;

/** Seconds of stats history a chart shows (150 samples at 2 s). */
export const STATS_WINDOW_S = 300;

/** Lines kept in a log view. */
export const MAX_LOG_LINES = 5000;

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

export type ContainerOp = 'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'remove';

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
  return `${host}${p.public}→${inner}`;
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
  for (const [id, p] of Object.entries(m.sample)) {
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

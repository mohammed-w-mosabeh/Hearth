// The inspector drawer's items (ROADMAP 8.7): "<kind>:<id>" in the hash's
// inspect parameter, so a drawer can be linked and survives a reload.

export type InspectKind = 'unit' | 'container' | 'package' | 'port' | 'proc' | 'alert';

export type Inspect =
  | { kind: 'unit'; scope: 'system' | 'user'; name: string }
  | { kind: 'container'; id: string }
  | { kind: 'package'; name: string }
  | { kind: 'port'; proto: 'tcp' | 'udp'; port: number }
  | { kind: 'proc'; group: string }
  | { kind: 'alert'; id: number };

const MAX = 512;

/** The item of an inspect parameter, or null when it is not one. */
export function parseInspect(v: string): Inspect | null {
  if (v.length === 0 || v.length > MAX || [...v].some((c) => c < ' ' || c === '\x7f')) return null;
  const i = v.indexOf(':');
  if (i < 0) return null;
  const kind = v.slice(0, i);
  const id = v.slice(i + 1);
  if (id === '') return null;
  switch (kind) {
    case 'unit': {
      const m = /^(system|user)\/(.+)$/.exec(id);
      return m?.[1] && m[2] && !m[2].includes('/') ? { kind, scope: m[1] as 'system' | 'user', name: m[2] } : null;
    }
    case 'container':
      return /^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(id) ? { kind, id } : null;
    case 'package':
      return /^[a-z0-9][a-z0-9+.-]*(:[a-z0-9-]+)?$/.test(id) ? { kind, name: id } : null;
    case 'port': {
      const m = /^(tcp|udp)\/(\d{1,5})$/.exec(id);
      const n = Number(m?.[2]);
      return m && n >= 1 && n <= 65535 ? { kind, proto: m[1] as 'tcp' | 'udp', port: n } : null;
    }
    case 'proc':
      return { kind, group: id };
    case 'alert':
      return /^[1-9]\d{0,15}$/.test(id) ? { kind, id: Number(id) } : null;
    default:
      return null;
  }
}

export function formatInspect(it: Inspect): string {
  switch (it.kind) {
    case 'unit':
      return `unit:${it.scope}/${it.name}`;
    case 'container':
      return `container:${it.id}`;
    case 'package':
      return `package:${it.name}`;
    case 'port':
      return `port:${it.proto}/${it.port}`;
    case 'proc':
      return `proc:${it.group}`;
    case 'alert':
      return `alert:${it.id}`;
  }
}

/** One link of a lineage chain: port → process → unit → package → container. */
export interface Link {
  label: string;
  item: Inspect | null; // null: shown, not linkable
}

/** Live points kept for a drawer's mini chart: about 10 minutes at 2 s. */
export const KEEP = 300;

/** points with [t, v] added, at most KEEP. */
export function push(points: [number, number][], t: number, v: number): [number, number][] {
  const last = points[points.length - 1];
  if (last && last[0] >= t) return points;
  const next = [...points, [t, v] as [number, number]];
  return next.length > KEEP ? next.slice(next.length - KEEP) : next;
}

/** Sparkline data for points. */
export function series(points: [number, number][]): [number[], number[]] {
  return [points.map((p) => p[0]), points.map((p) => p[1])];
}

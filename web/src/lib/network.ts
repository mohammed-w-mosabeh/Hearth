// Network page: sockets, addressing, NetworkManager and firewall state
// (internal/netinfo), and the pure helpers the page uses.

import type { Container } from './docker';

export type Scope = 'loopback' | 'address' | 'all';

export interface Owner {
  pid?: number;
  start?: number;
  name?: string;
  exact?: boolean;
  group?: string;
  kind?: string;
  group_name?: string;
  unit?: string;
  container?: string;
}

export interface Endpoint {
  proto: 'tcp' | 'udp';
  address: string;
  port: number;
  scope: Scope;
  remote?: string;
  uid: number;
  user?: string;
  owner?: Owner;
  sent?: number;
  received?: number;
}

export interface Sockets {
  listeners: Endpoint[];
  connections: Endpoint[];
  truncated?: boolean;
}

export interface Interface {
  name: string;
  kind: 'loopback' | 'wifi' | 'ethernet' | 'bridge' | 'virtual';
  mac?: string;
  mtu: number;
  up: boolean;
  addrs: string[];
}

export interface NMConn {
  id: string;
  type: string;
  vpn?: boolean;
  default?: boolean;
  state: string;
  devices: string[];
  ssid?: string;
  signal?: number;
}

export interface Firewall {
  name: 'ufw' | 'firewalld' | 'nftables';
  installed: boolean;
  active: boolean;
}

export interface NetState {
  interfaces: Interface[];
  gateways: { gateway: string; interface: string }[];
  dns: string[];
  dns_stub?: boolean;
  nm: { running: boolean; state?: string; connectivity?: string; connections: NMConn[] };
  firewalls: Firewall[];
}

export interface PkgRef {
  name: string;
  id: string;
  version?: string;
}

export interface Lineage {
  unit?: {
    name: string;
    scope: 'system' | 'user';
    description?: string;
    active?: string;
    sub?: string;
    file?: string;
    main_pid?: number;
  };
  exe?: string;
  exe_exact?: boolean;
  exe_package?: PkgRef;
  unit_package?: PkgRef;
}

/** One listening row: the sockets of one owner on one port (0.0.0.0 and ::, or 127.0.0.1 and ::1, are one row). */
export interface Row {
  key: string;
  proto: 'tcp' | 'udp';
  port: number;
  addresses: string[];
  scope: Scope; // the widest of its sockets
  user: string;
  owner?: Owner;
}

const width: Record<Scope, number> = { loopback: 0, address: 1, all: 2 };

/** The owner's display name: its group, else its process, else "". */
export function ownerName(o: Owner | undefined): string {
  return o?.group_name || o?.name || '';
}

/** Listening sockets merged per protocol, port and owner, sorted by port. */
export function rows(listeners: Endpoint[]): Row[] {
  const by = new Map<string, Row>();
  for (const e of listeners) {
    const who = e.owner?.group || (e.owner?.pid ? `pid:${e.owner.pid}` : `uid:${e.uid}`);
    const key = `${e.proto}|${e.port}|${who}`;
    const r = by.get(key);
    if (!r) {
      by.set(key, { key, proto: e.proto, port: e.port, addresses: [e.address], scope: e.scope, user: e.user ?? String(e.uid), ...(e.owner ? { owner: e.owner } : {}) });
      continue;
    }
    if (!r.addresses.includes(e.address)) r.addresses.push(e.address);
    if (width[e.scope] > width[r.scope]) r.scope = e.scope;
  }
  return [...by.values()].sort((a, b) => a.port - b.port || a.proto.localeCompare(b.proto) || ownerName(a.owner).localeCompare(ownerName(b.owner)));
}

export type ScopeFilter = 'all' | 'exposed' | 'local';

/** Rows matching the scope filter and every search word (port, address, owner, user). */
export function filterRows(list: Row[], scope: ScopeFilter, search: string): Row[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return list.filter((r) => {
    if (scope === 'exposed' && r.scope === 'loopback') return false;
    if (scope === 'local' && r.scope !== 'loopback') return false;
    const hay = `${r.port} ${r.proto} ${r.addresses.join(' ')} ${ownerName(r.owner)} ${r.owner?.name ?? ''} ${r.owner?.unit ?? ''} ${r.user}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

/** Connections matching every search word (addresses, ports, owner). */
export function filterConnections(list: Endpoint[], search: string): Endpoint[] {
  const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return list;
  return list.filter((c) => {
    const hay = `${c.proto} ${hostPort(c.address, c.port)} ${c.remote ?? ''} ${ownerName(c.owner)} ${c.owner?.name ?? ''} ${c.user ?? ''}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

/** "addr:port", with IPv6 addresses in brackets. */
export function hostPort(address: string, port: number): string {
  return address.includes(':') ? `[${address}]:${port}` : `${address}:${port}`;
}

/** Key of a connection across reads. */
export const connKey = (c: Endpoint) => `${c.proto}|${hostPort(c.address, c.port)}|${c.remote ?? ''}`;

/**
 * Throughput of each TCP connection between two reads dt seconds apart, in
 * bytes/s, by connKey. Connections new since the last read are left out.
 */
export function connRates(prev: Map<string, Endpoint>, cur: Endpoint[], dt: number): Map<string, { up: number; down: number }> {
  const out = new Map<string, { up: number; down: number }>();
  if (dt <= 0) return out;
  for (const c of cur) {
    const p = prev.get(connKey(c));
    if (!p || c.proto !== 'tcp') continue;
    const up = (c.sent ?? 0) - (p.sent ?? 0);
    const down = (c.received ?? 0) - (p.received ?? 0);
    if (up >= 0 && down >= 0) out.set(connKey(c), { up: up / dt, down: down / dt });
  }
  return out;
}

/**
 * The container behind a row: the owner's own cgroup (a process inside the
 * container), else a container publishing that port (Docker's proxy listens
 * for it).
 */
export function containerOf(r: Row, containers: Container[]): Container | null {
  const id = r.owner?.container;
  if (id) return containers.find((c) => c.id.startsWith(id)) ?? null;
  return containers.find((c) => c.ports.some((p) => p.public === r.port && p.type === r.proto)) ?? null;
}

// Ports whose protocol is known not to be HTTP: no "open in browser".
const notHTTP = new Set([21, 22, 23, 25, 53, 67, 68, 110, 111, 123, 137, 138, 139, 143, 389, 445, 465, 514, 546, 547, 587, 636, 993, 995, 1716, 1883, 3306, 5353, 5432, 5900, 6379, 11211, 27017]);

/** A URL to try a TCP listener in the browser, or null. */
export function browserURL(r: Row): string | null {
  if (r.proto !== 'tcp' || notHTTP.has(r.port)) return null;
  if (r.scope !== 'address') return `http://127.0.0.1:${r.port}/`;
  const a = r.addresses[0] ?? '';
  if (a.startsWith('fe80:')) return null; // needs a zone
  return `http://${hostPort(a, r.port)}/`;
}

/** The unit scope of an owner for lineage and unit actions, or null. */
export function unitScope(o: Owner | undefined): 'system' | 'user' | null {
  if (!o?.unit) return null;
  if (o.kind === 'service' || o.kind === 'container') return 'system';
  if (o.kind === 'user-service' || o.kind === 'app') return 'user';
  return null;
}

/** The lineage query of an owner, or null when there is nothing to ask. */
export function lineageQuery(o: Owner | undefined): string | null {
  if (!o) return null;
  const q = new URLSearchParams();
  const scope = unitScope(o);
  if (scope && o.unit) {
    q.set('scope', scope);
    q.set('unit', o.unit);
  }
  if (o.pid && o.start) {
    q.set('pid', String(o.pid));
    q.set('start', String(o.start));
  }
  const s = q.toString();
  return s ? s : null;
}

/** The connection NM routes through by default, if any. */
export function primary(nm: NetState['nm']): NMConn | null {
  return nm.connections.find((c) => c.default) ?? null;
}

/** VPN connections: NM's VPN and WireGuard connections. */
export function vpns(nm: NetState['nm']): NMConn[] {
  return nm.connections.filter((c) => c.vpn || c.type === 'vpn' || c.type === 'wireguard');
}

/** Interfaces worth showing by default: up, with an address, not loopback or a container's veth. */
export function mainInterfaces(list: Interface[]): Interface[] {
  return list.filter((i) => i.up && i.kind !== 'loopback' && i.addrs.length > 0 && !i.name.startsWith('veth'));
}

/** The packages of a lineage: the program's first, then the unit file's when it differs. */
export function lineagePackages(l: Lineage | null | undefined): PkgRef[] {
  const out: PkgRef[] = [];
  for (const p of [l?.exe_package, l?.unit_package]) if (p && !out.some((q) => q.id === p.id)) out.push(p);
  return out;
}

import { describe, expect, it } from 'vitest';
import type { Container } from './docker';
import {
  browserURL,
  connKey,
  connRates,
  containerOf,
  filterConnections,
  filterRows,
  hostPort,
  lineageQuery,
  mainInterfaces,
  primary,
  rows,
  vpns,
  type Endpoint,
  type Interface,
  type Owner,
  type Row,
} from './network';

const ssh: Owner = { pid: 300, start: 5000, name: 'sshd', group: 'unit:ssh.service', kind: 'service', group_name: 'ssh', unit: 'ssh.service' };
const listeners: Endpoint[] = [
  { proto: 'tcp', address: '::', port: 22, scope: 'all', uid: 0, user: 'root', owner: ssh },
  { proto: 'tcp', address: '0.0.0.0', port: 22, scope: 'all', uid: 0, user: 'root', owner: ssh },
  { proto: 'tcp', address: '127.0.0.1', port: 631, scope: 'loopback', uid: 0, user: 'root', owner: { group: 'unit:cups.service', group_name: 'cups', kind: 'service', unit: 'cups.service' } },
  { proto: 'udp', address: '0.0.0.0', port: 5353, scope: 'all', uid: 1000, user: 'me', owner: { pid: 200, start: 9, name: 'chat', exact: true, group: 'app:org.example.Chat', kind: 'app', group_name: 'Chat', unit: 'app-gnome-org.example.Chat-200.scope' } },
  { proto: 'tcp', address: '127.0.0.1', port: 8080, scope: 'loopback', uid: 0, user: 'root', owner: { group: 'unit:docker.service', group_name: 'docker', kind: 'service', unit: 'docker.service', name: 'docker-proxy', pid: 7, start: 1 } },
  { proto: 'tcp', address: '192.168.1.5', port: 9000, scope: 'address', uid: 999 },
  { proto: 'tcp', address: '127.0.0.53', port: 53, scope: 'loopback', uid: 991, user: 'systemd-resolve' },
  { proto: 'tcp', address: '0.0.0.0', port: 53, scope: 'all', uid: 991, user: 'systemd-resolve' },
];

const at = (port: number): Row => {
  const x = rows(listeners).find((y) => y.port === port);
  if (!x) throw new Error(`no row for ${port}`);
  return x;
};

describe('rows', () => {
  const r = rows(listeners);
  it('merges an owner’s sockets on one port and keeps the widest scope', () => {
    expect(r.map((x) => `${x.proto}/${x.port}`)).toEqual(['tcp/22', 'tcp/53', 'tcp/631', 'udp/5353', 'tcp/8080', 'tcp/9000']);
    expect(r[0]?.addresses).toEqual(['::', '0.0.0.0']);
    expect(r[1]?.scope).toBe('all');
    expect(r[1]?.addresses).toEqual(['127.0.0.53', '0.0.0.0']);
  });
  it('filters by scope and search', () => {
    expect(filterRows(r, 'local', '').map((x) => x.port)).toEqual([631, 8080]);
    expect(filterRows(r, 'exposed', '').map((x) => x.port)).toEqual([22, 53, 5353, 9000]);
    expect(filterRows(r, 'all', 'CUPS').map((x) => x.port)).toEqual([631]);
    expect(filterRows(r, 'all', 'chat 5353').map((x) => x.port)).toEqual([5353]);
    expect(filterRows(r, 'all', '192.168').map((x) => x.port)).toEqual([9000]);
  });
});

describe('browserURL', () => {
  it('opens TCP listeners on loopback, everything or one address', () => {
    expect(browserURL(at(8080))).toBe('http://127.0.0.1:8080/');
    expect(browserURL(at(9000))).toBe('http://192.168.1.5:9000/');
    expect(browserURL({ key: '', proto: 'tcp', port: 80, scope: 'address', addresses: ['fd00::1'], user: '' })).toBe('http://[fd00::1]:80/');
  });
  it('skips UDP, known non-HTTP ports and link-local addresses', () => {
    expect(browserURL(at(5353))).toBeNull();
    expect(browserURL(at(22))).toBeNull();
    expect(browserURL({ key: '', proto: 'tcp', port: 80, scope: 'address', addresses: ['fe80::1'], user: '' })).toBeNull();
  });
});

describe('containerOf', () => {
  const c = (id: string, ports: Container['ports']) => ({ id, name: id, ports }) as unknown as Container;
  const containers = [c('aaaa1111', [{ private: 80, public: 8080, type: 'tcp', ip: '127.0.0.1' }]), c('bbbb2222', [])];
  it('matches a published port or the owner’s container', () => {
    expect(containerOf(at(8080), containers)?.id).toBe('aaaa1111');
    expect(containerOf(at(22), containers)).toBeNull();
    expect(containerOf({ key: '', proto: 'tcp', port: 1, scope: 'all', addresses: [], user: '', owner: { container: 'bbbb' } }, containers)?.id).toBe('bbbb2222');
  });
});

describe('lineageQuery', () => {
  it('asks for the unit in its scope and the process', () => {
    expect(lineageQuery(ssh)).toBe('scope=system&unit=ssh.service&pid=300&start=5000');
    expect(lineageQuery(listeners[3]?.owner)).toBe('scope=user&unit=app-gnome-org.example.Chat-200.scope&pid=200&start=9');
    expect(lineageQuery({ pid: 4, start: 2, kind: 'session', name: 'nc' })).toBe('pid=4&start=2');
    expect(lineageQuery(undefined)).toBeNull();
    expect(lineageQuery({ kind: 'kernel' })).toBeNull();
  });
});

describe('connections', () => {
  const a: Endpoint = { proto: 'tcp', address: '192.168.1.5', port: 22, scope: 'address', remote: '192.168.1.9:50000', uid: 0, sent: 1000, received: 500, owner: ssh };
  const b: Endpoint = { proto: 'tcp', address: 'fd00::5', port: 443, scope: 'address', remote: '[2001:db8::1]:443', uid: 1000, sent: 10, received: 10 };
  it('computes per-connection rates between reads', () => {
    const prev = new Map([[connKey(a), a]]);
    const rates = connRates(prev, [{ ...a, sent: 3000, received: 1500 }, b], 2);
    expect(rates.get(connKey(a))).toEqual({ up: 1000, down: 500 });
    expect(rates.has(connKey(b))).toBe(false);
    expect(connRates(prev, [{ ...a, sent: 0 }], 2).size).toBe(0); // counter reset (a new socket on the same addresses)
  });
  it('filters connections', () => {
    expect(filterConnections([a, b], 'ssh')).toEqual([a]);
    expect(filterConnections([a, b], '2001:db8')).toEqual([b]);
    expect(hostPort('fd00::5', 443)).toBe('[fd00::5]:443');
  });
});

describe('state helpers', () => {
  const nm = {
    running: true,
    connections: [
      { id: 'br-1', type: 'bridge', state: 'activated', devices: ['br-1'] },
      { id: 'Home', type: '802-11-wireless', default: true, state: 'activated', devices: ['wlp3s0'], ssid: 'Home', signal: 70 },
      { id: 'Work', type: 'vpn', vpn: true, state: 'activated', devices: [] },
      { id: 'wg0', type: 'wireguard', state: 'activated', devices: ['wg0'] },
    ],
  };
  it('finds the primary connection and VPNs', () => {
    expect(primary(nm)?.id).toBe('Home');
    expect(vpns(nm).map((c) => c.id)).toEqual(['Work', 'wg0']);
    expect(primary({ running: false, connections: [] })).toBeNull();
  });
  it('keeps the interfaces that matter', () => {
    const i = (name: string, kind: Interface['kind'], up: boolean, addrs: string[]): Interface => ({ name, kind, up, addrs, mtu: 1500 });
    const list = [i('lo', 'loopback', true, ['127.0.0.1/8']), i('wlp3s0', 'wifi', true, ['192.168.1.5/24']), i('eno1', 'ethernet', true, []), i('veth1', 'virtual', true, ['fe80::1/64']), i('docker0', 'bridge', true, ['172.17.0.1/16'])];
    expect(mainInterfaces(list).map((x) => x.name)).toEqual(['wlp3s0', 'docker0']);
  });
});

// Invented API answers for the design screenshots (ROADMAP 8.1). Nothing here
// comes from a real machine, so the images in docs/design/screens hold no
// project, host or user names.
const now = Date.UTC(2026, 10, 2, 12, 0, 0);
const GB = 1024 ** 3;

export const sample = {
  t: new Date(now).toISOString(),
  cpu: { usage: 23, cores: [31, 18, 25, 19, 22, 28, 16, 20], freq_mhz: [2400], load1: 0.9, load5: 1.1, load15: 1.0 },
  mem: { total: 32 * GB, available: 19 * GB, used: 13 * GB, cached: 6 * GB, swap_total: 8 * GB, swap_used: 0 },
  disks: [{ mount: '/', device: 'nvme0n1p2', fstype: 'ext4', total: 512 * GB, used: 281 * GB, avail: 205 * GB, pct: 58 }],
  disk_io: [{ name: 'nvme0n1', read_bps: 120000, write_bps: 480000, busy_pct: 3 }],
  net: [{ name: 'wlan0', rx_bps: 182000, tx_bps: 41000, virtual: false }],
  temps: [{ chip: 'coretemp', label: 'Package', c: 54, max_c: 100 }],
  fans: [{ chip: 'thinkpad', label: 'fan1', rpm: 2300 }],
  gpus: [],
  power: {
    batteries: [{ id: 'BAT0', pct: 81, state: 'discharging', rate_w: 9.4, energy_wh: 42, full_wh: 52, design_wh: 57, health_pct: 91, to_empty_s: 16000 }],
    on_ac: false,
    profile: { active: 'balanced', available: ['power-saver', 'balanced', 'performance'] },
  },
  cpu_temp_c: 54,
};

const container = (id, name, project, service, state, status, health, port) => ({
  id: id.repeat(64).slice(0, 64),
  name,
  image: `example/${service}:1.4`,
  image_id: 'sha256:' + id.repeat(64).slice(0, 64),
  command: '/entrypoint',
  created: now / 1000 - 86400 * 9,
  state,
  status,
  health,
  ports: port ? [{ ip: port[0], private: port[1], public: port[2], type: 'tcp' }] : [],
  networks: [`${project}_default`],
  mounts: [{ type: 'volume', name: `${project}_data`, destination: '/data', rw: true }],
  project,
  service,
});

export const snapshot = {
  kind: 'snapshot',
  state: 'ok',
  version: '29.0.0',
  time: now,
  containers: [
    container('a1', 'notes-web-1', 'notes', 'web', 'running', 'Up 9 days', 'healthy', ['127.0.0.1', 8080, 8101]),
    container('a2', 'notes-db-1', 'notes', 'db', 'running', 'Up 9 days', 'healthy'),
    container('b1', 'photos-web-1', 'photos', 'web', 'running', 'Up 2 days', 'unhealthy', ['0.0.0.0', 8080, 8102]),
    container('b2', 'photos-worker-1', 'photos', 'worker', 'running', 'Up 2 days'),
    container('c1', 'feeds-1', 'feeds', 'feeds', 'exited', 'Exited (0) 3 days ago'),
  ],
  images: [],
  networks: [],
  volumes: [],
  projects: [
    { name: 'notes', working_dir: '/srv/notes', config_files: ['/srv/notes/compose.yaml'], services: ['web', 'db'], containers: [], running: 2 },
    { name: 'photos', working_dir: '/srv/photos', config_files: ['/srv/photos/compose.yaml'], services: ['web', 'worker'], containers: [], running: 2 },
    { name: 'feeds', working_dir: '/srv/feeds', config_files: ['/srv/feeds/compose.yaml'], services: ['feeds'], containers: [], running: 0 },
  ],
};

const unit = (name, description, active, sub, enabled, scope = 'system') => ({ name, scope, description, load: 'loaded', active, sub, enabled });
export const services = {
  kind: 'list',
  units: [
    unit('bluetooth.service', 'Bluetooth service', 'active', 'running', 'enabled'),
    unit('cron.service', 'Regular background program processing', 'active', 'running', 'enabled'),
    unit('cups.service', 'CUPS Scheduler', 'inactive', 'dead', 'disabled'),
    unit('docker.service', 'Docker Application Container Engine', 'active', 'running', 'enabled'),
    unit('example-sync.service', 'Example sync job', 'failed', 'failed', 'enabled'),
    unit('NetworkManager.service', 'Network Manager', 'active', 'running', 'enabled'),
    unit('ssh.service', 'OpenBSD Secure Shell server', 'active', 'running', 'enabled'),
    unit('hearthd.service', 'Hearth', 'active', 'running', 'enabled', 'user'),
  ],
};

const listener = (proto, address, port, name, unitName) => ({ proto, address, port, scope: 'system', uid: 0, user: 'root', owner: { name, unit: unitName } });
const accessUsers = [
  { name: 'alex', uid: 1000, login: true, groups: ['alex', 'sudo', 'docker'], sudo: true, docker: true },
  { name: 'guest', uid: 1001, login: true, groups: ['guest'] },
];

const checks = [
  { id: 'listeners', status: 'warning', findings: [{ code: 'listener.exposed', severity: 'warning', subject: '0.0.0.0:8102' }] },
  { id: 'firewall', status: 'ok', findings: [] },
  { id: 'ssh', status: 'ok', findings: [] },
  { id: 'updates', status: 'warning', findings: [{ code: 'updates.security', severity: 'warning', subject: '3' }] },
  { id: 'unattended', status: 'ok', findings: [] },
  { id: 'restart', status: 'ok', findings: [] },
  { id: 'cves', status: 'off', findings: [] },
  { id: 'containers', status: 'ok', findings: [] },
  { id: 'path', status: 'ok', findings: [] },
];

/** Answers by API path. A path missing here fails the capture. */
export const api = {
  '/session': { ok: true },
  '/app-health': [],
  '/modes': { modes: [], approvals: [] },
  '/automations/approvals': { automations: [] },
  '/automations/pause': { paused: false },
  '/apps': {
    apps: [
      { id: 'org.example.Notes', name: 'Notes', generic_name: 'Text editor', comment: 'Write plain notes', categories: ['Utility'], source: 'system', path: '/usr/share/applications/org.example.Notes.desktop', has_icon: false },
      { id: 'org.example.Photos', name: 'Photos', generic_name: 'Image viewer', categories: ['Graphics'], source: 'flatpak', path: '/var/lib/flatpak/exports/share/applications/org.example.Photos.desktop', has_icon: false },
    ],
  },
  '/apps/running': { running: ['org.example.Notes'] },
  '/settings/apps.favorites': { value: { ids: ['org.example.Photos'] } },
  '/docker/usage': {
    available: true,
    running: [
      { id: 'b1'.repeat(32), name: 'photos-web-1', cpu: 6.2, mem: 0.8 * GB },
      { id: 'a1'.repeat(32), name: 'notes-web-1', cpu: 1.1, mem: 0.3 * GB },
      { id: 'b2'.repeat(32), name: 'photos-worker-1', cpu: 0.7, mem: 0.2 * GB },
      { id: 'a2'.repeat(32), name: 'notes-db-1', cpu: 0.4, mem: 0.1 * GB },
    ],
    stopped: 1,
    unexpected: [],
    time: now,
  },
  '/docker': snapshot,
  '/homepage': {
    tiles: [
      { id: 1, name: 'Notes', url: 'http://127.0.0.1:8101', description: '', icon: 'builtin:code', group: 'Apps', container: '', health_check: true, check_port: 0, position: 0 },
      { id: 2, name: 'Photos', url: 'http://127.0.0.1:8102', description: '', icon: 'builtin:cloud', group: 'Apps', container: '', health_check: true, check_port: 0, position: 1 },
    ],
    status: { 1: { state: 'up', latency_ms: 12, time: now }, 2: { state: 'warn', code: 503, latency_ms: 840, time: now } },
  },
  '/forecasts': { disks: [], batteries: {} },
  '/metrics/live': { t: [], series: {} },
  '/metrics/history': { t: [], series: {} },
  '/restart': { reboot: false, services: [] },
  // Home's health ring and Needs attention (ROADMAP 8.6).
  '/services': services,
  '/alerts/events': { events: [] },
  '/disks/health': { status: 'ok', disks: [] },
  '/settings/home.snoozed': { value: null },
  '/settings/home.layout': { value: null },
  '/net/sockets': {
    listeners: [
      listener('tcp', '127.0.0.1', 7380, 'hearthd', 'hearthd.service'),
      listener('tcp', '0.0.0.0', 22, 'sshd', 'ssh.service'),
      listener('tcp', '0.0.0.0', 8102, 'docker-proxy', 'docker.service'),
      listener('udp', '127.0.0.1', 5353, 'resolved', 'systemd-resolved.service'),
    ],
    connections: [],
  },
  '/net/state': {
    interfaces: [{ name: 'wlan0', kind: 'wifi', mtu: 1500, up: true, addrs: ['192.0.2.20/24'] }],
    gateways: [{ gateway: '192.0.2.1', interface: 'wlan0' }],
    dns: ['192.0.2.1'],
    nm: { running: true, state: 'connected', connectivity: 'full', connections: [{ id: 'Example Wi-Fi', type: 'wifi', default: true, state: 'activated', devices: ['wlan0'], ssid: 'Example', signal: 72 }] },
    firewalls: [{ name: 'ufw', installed: true, active: true }],
  },
  '/connectivity': {
    config: { enabled: true, targets: [{ host: 'example.org', port: 443 }] },
    checks: [{ name: 'gateway', kind: 'gateway', ok: true, ms: 3 }, { name: 'example.org:443', kind: 'target', ok: true, ms: 41 }],
    down: false,
    outages: [],
    usage: [{ iface: 'wlan0', today_rx: 2 * GB, today_tx: 0.4 * GB, month_rx: 41 * GB, month_tx: 6 * GB }],
  },
  '/firewall': { installed: true, active: true, incoming: 'deny', rules: [{ action: 'allow', proto: 'tcp', port: 22, family: 'both', editable: true, text: '22/tcp ALLOW IN Anywhere' }] },
  '/security': { time: now, checks, cves: { installed: false, list: [], fixed: 0, remote: 0 }, counts: { ok: 6, warning: 2, off: 1 } },
  '/security/access': {
    days: 30,
    sources: [{ source: '192.0.2.44', accepted: 12, failed: 1, last: now - 7200000 }],
    byDay: [{ day: '2026-11-02', accepted: 2, failed: 0 }],
    sessions: [{ id: '2', user: 'alex', uid: 1000, seat: 'seat0', type: 'wayland', state: 'active', remote: false, since: now - 3600000 }],
    users: accessUsers,
  },
  '/security/integrity': { since: now - 86400000, scanned: 41, watched: 41, changes: [], system: [{ dir: '/etc/cron.d', readable: true, names: ['e2scrub_all'] }] },
  '/security/lynis': {
    installed: true,
    schedule: 'monthly',
    running: false,
    report: { time: now - 86400000, index: 72, version: '3.1', warnings: [], suggestions: [{ test: 'SSH-7408', text: 'Harden the SSH configuration' }] },
    history: [{ time: now - 86400000, index: 72, warnings: 0, suggestions: 1 }],
  },
};

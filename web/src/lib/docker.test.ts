import { describe, expect, it } from 'vitest';
import {
  appendLogs,
  available,
  columns,
  envPairs,
  pruneTotal,
  finished,
  projectOps,
  projectState,
  projectView,
  type Project,
  imageTitle,
  imageView,
  layerCommand,
  shortID,
  uniqueSize,
  vulnURL,
  type Image,
  execURL,
  exitCode,
  filterLogs,
  imageName,
  latest,
  MAX_LOG_LINES,
  mergeStats,
  portText,
  portURL,
  ports,
  problemCount,
  tone,
  view,
  type Container,
  type LogLine,
  type Point,
} from './docker';

const c = (name: string, state: string, extra: Partial<Container> = {}): Container => ({
  id: `${name}-id`, name, image: 'nginx', image_id: 'sha256:x', command: '', created: 0, state,
  status: state === 'exited' ? 'Exited (0) 1 hour ago' : 'Up 1 hour', ports: [], networks: [], mounts: [], ...extra,
});

const pt = (t: number, extra: Partial<Point> = {}): Point => ({
  t, cpu: 0, mem: 0, mem_limit: 0, net_rx: 0, net_tx: 0, blk_read: 0, blk_write: 0, pids: 0, ...extra,
});

describe('containers', () => {
  it('reads tone and exit codes', () => {
    expect(tone(c('a', 'running'))).toBe('running');
    expect(tone(c('a', 'running', { health: 'unhealthy' }))).toBe('failed');
    expect(tone(c('a', 'running', { health: 'starting' }))).toBe('changing');
    expect(tone(c('a', 'paused'))).toBe('paused');
    expect(tone(c('a', 'exited'))).toBe('stopped');
    expect(tone(c('a', 'exited', { status: 'Exited (1) 2 hours ago' }))).toBe('failed');
    expect(tone(c('a', 'exited', { status: 'Exited (128) 2 hours ago' }))).toBe('failed');
    expect(tone(c('a', 'exited', { status: 'Exited (143) 2 hours ago' }))).toBe('stopped');
    expect(tone(c('a', 'exited', { status: 'Exited (137) 2 hours ago' }))).toBe('stopped');
    expect(tone(c('a', 'created', { status: 'Created' }))).toBe('stopped');
    expect(exitCode('Exited (-1) now')).toBe(-1);
    expect(exitCode('Up 2 hours')).toBeNull();
  });

  it('offers actions by state', () => {
    expect(available(c('a', 'running'))).toEqual(['stop', 'restart', 'pause', 'remove']);
    expect(available(c('a', 'paused'))).toEqual(['unpause', 'stop', 'remove']);
    expect(available(c('a', 'exited'))).toEqual(['start', 'remove']);
    expect(available(c('a', 'removing'))).toEqual([]);
  });

  it('formats ports', () => {
    const list = ports(c('a', 'running', {
      ports: [
        { private: 9000, type: 'tcp' },
        { ip: '0.0.0.0', private: 80, public: 8080, type: 'tcp' },
        { ip: '::', private: 80, public: 8080, type: 'tcp' },
        { ip: '127.0.0.1', private: 53, public: 5353, type: 'udp' },
      ],
    }));
    expect(list.map(portText)).toEqual(['127.0.0.1:5353→53/udp', '8080→80/tcp', '9000/tcp']);
    expect(portText({ ip: '::1', private: 1, public: 2, type: 'tcp' })).toBe('[::1]:2→1/tcp');
    expect(list.map(portURL)).toEqual([null, 'http://127.0.0.1:8080/', null]);
    expect(portURL({ ip: '192.168.1.5', private: 80, public: 80, type: 'tcp' })).toBeNull();
  });

  it('shortens image names', () => {
    expect(imageName(c('a', 'running', { image: 'sha256:0123456789abcdef0123' }))).toBe('0123456789ab');
    expect(imageName(c('a', 'running', { image: `nginx@sha256:${'a'.repeat(64)}` }))).toBe('nginx');
  });

  it('filters, searches and sorts', () => {
    const list = [
      c('web', 'running', { project: 'shop' }),
      c('db', 'exited', { status: 'Exited (1) now', project: 'shop' }),
      c('cache', 'paused'),
      c('job', 'created', { status: 'Created' }),
    ];
    const names = (l: Container[]) => l.map((x) => x.name);
    expect(names(view(list, {}, 'all', '', 'name', false))).toEqual(['cache', 'db', 'job', 'web']);
    expect(names(view(list, {}, 'running', '', 'name', false))).toEqual(['cache', 'web']);
    expect(names(view(list, {}, 'stopped', '', 'name', false))).toEqual(['db', 'job']);
    expect(names(view(list, {}, 'problem', '', 'name', false))).toEqual(['db']);
    expect(names(view(list, {}, 'all', 'SHOP', 'name', false))).toEqual(['db', 'web']);
    expect(names(view(list, {}, 'all', '', 'state', false))).toEqual(['db', 'web', 'cache', 'job']);
    const stats = { 'web-id': pt(1, { mem: 50 }), 'cache-id': pt(1, { mem: 90 }) };
    expect(names(view(list, stats, 'all', '', 'mem', true))).toEqual(['cache', 'web', 'db', 'job']);
    expect(problemCount(list)).toBe(1);
  });
});

describe('logs', () => {
  const l = (text: string, stream: LogLine['stream'] = 'stdout'): LogLine => ({ stream, text });
  it('appends, resets and caps', () => {
    let lines = appendLogs([], { kind: 'lines', reset: true, lines: [l('a')] });
    lines = appendLogs(lines, { kind: 'lines', lines: [l('b', 'stderr')] });
    expect(lines.map((x) => x.text)).toEqual(['a', 'b']);
    expect(appendLogs(lines, { kind: 'end' })).toBe(lines);
    expect(appendLogs(lines, { kind: 'end', reset: true })).toEqual([]);
    expect(appendLogs(lines, { kind: 'lines', reset: true, lines: [l('c')] }).map((x) => x.text)).toEqual(['c']);
    const many = Array.from({ length: MAX_LOG_LINES }, (_, i) => l(String(i)));
    const capped = appendLogs(many, { kind: 'lines', lines: [l('new')] });
    expect(capped.length).toBe(MAX_LOG_LINES);
    expect(capped.at(-1)?.text).toBe('new');
    expect(capped[0]?.text).toBe('1');
  });
  it('filters', () => {
    const lines = [l('GET /a 200'), l('Error: boom', 'stderr'), l('get /b 500', 'stderr')];
    expect(filterLogs(lines, 'get', false).length).toBe(2);
    expect(filterLogs(lines, '', true).length).toBe(2);
    expect(filterLogs(lines, 'get 500', true).map((x) => x.text)).toEqual(['get /b 500']);
    expect(filterLogs(lines, ' ', false)).toBe(lines);
  });
});

describe('stats', () => {
  it('merges history and samples', () => {
    let h = mergeStats({}, { kind: 'history', history: { a: [pt(1000)] } });
    h = mergeStats(h, { kind: 'sample', sample: { a: pt(3000, { cpu: 5 }), b: pt(3000) } });
    h = mergeStats(h, { kind: 'sample', sample: { a: pt(3000, { cpu: 5 }) } }); // a repeat
    expect(h.a?.length).toBe(2);
    expect(h.b?.length).toBe(1);
    expect(latest(h).a?.cpu).toBe(5);
    expect(columns(h.a ?? [], 'cpu', 'mem')).toEqual([[1, 3], [0, 5], [0, 0]]);
  });
  it('caps history', () => {
    let h: Record<string, Point[]> = {};
    for (let i = 0; i < 200; i++) h = mergeStats(h, { kind: 'sample', sample: { a: pt(i) } });
    expect(h.a?.length).toBe(150);
    expect(h.a?.[0]?.t).toBe(50);
  });
});

describe('env', () => {
  it('splits pairs', () => {
    expect(envPairs(['A=1', 'B=x=y', 'C', 'D_PASSWORD=••••'])).toEqual([['A', '1'], ['B', 'x=y'], ['C', ''], ['D_PASSWORD', '••••']]);
    expect(envPairs(null)).toEqual([]);
  });
});

describe('exec', () => {
  it('builds the shell URL', () => {
    expect(execURL('http://127.0.0.1:7171/#/docker', 'web', 80, 24)).toBe('ws://127.0.0.1:7171/api/v1/docker/exec/web?cols=80&rows=24');
    expect(execURL('https://h:1/', 'a/b', 1, 2)).toBe('wss://h:1/api/v1/docker/exec/a%2Fb?cols=1&rows=2');
  });
});

describe('images', () => {
  const im = (id: string, tags: string[], extra: Partial<Image> = {}): Image => ({
    id: `sha256:${id.padEnd(64, '0')}`, tags, digests: [], created: 0, size: 100, shared_size: -1,
    dangling: tags.length === 0, containers: [], ...extra,
  });
  it('names and sizes', () => {
    expect(imageTitle(im('abc', ['nginx:latest']))).toBe('nginx:latest');
    expect(imageTitle(im('abcdef123456789', []))).toBe('abcdef123456');
    expect(shortID('sha256:0123456789abcdef')).toBe('0123456789ab');
    expect(uniqueSize(im('a', [], { shared_size: 30 }))).toBe(70);
    expect(uniqueSize(im('a', []))).toBeNull();
  });
  it('filters and sorts', () => {
    const list = [
      im('1', ['redis:7'], { containers: ['c'], size: 50, created: 3 }),
      im('2', ['nginx:latest'], { size: 300, created: 1 }),
      im('3', [], { size: 10, created: 2 }),
    ];
    const t = (l: Image[]) => l.map(imageTitle);
    expect(t(imageView(list, 'all', '', 'name', false))).toEqual(['300000000000', 'nginx:latest', 'redis:7']);
    expect(t(imageView(list, 'used', '', 'name', false))).toEqual(['redis:7']);
    expect(t(imageView(list, 'unused', '', 'size', true))).toEqual(['nginx:latest', '300000000000']);
    expect(t(imageView(list, 'dangling', '', 'name', false))).toEqual(['300000000000']);
    expect(t(imageView(list, 'all', 'NGI', 'name', false))).toEqual(['nginx:latest']);
    expect(t(imageView(list, 'all', '', 'created', true))).toEqual(['redis:7', '300000000000', 'nginx:latest']);
  });
  it('cleans layer commands', () => {
    expect(layerCommand('/bin/sh -c #(nop)  CMD ["nginx" "-g"]')).toBe('CMD ["nginx" "-g"]');
    expect(layerCommand('/bin/sh -c apt-get update &&   apt-get install x')).toBe('RUN apt-get update && apt-get install x');
    expect(layerCommand('RUN /bin/sh -c echo hi # buildkit')).toBe('RUN /bin/sh -c echo hi # buildkit');
  });
  it('links known vulnerability IDs only', () => {
    expect(vulnURL('CVE-2024-12345')).toBe('https://nvd.nist.gov/vuln/detail/CVE-2024-12345');
    expect(vulnURL('GHSA-jfh8-c2jp-5v3q')).toBe('https://github.com/advisories/GHSA-jfh8-c2jp-5v3q');
    expect(vulnURL('javascript:alert(1)')).toBeNull();
    expect(vulnURL('DLA-1234-1')).toBeNull();
  });
});

describe('compose projects', () => {
  const p = (name: string, running: number, n: number, extra: Partial<Project> = {}): Project => ({
    name, config_files: [], services: [], containers: Array.from({ length: n }, (_, i) => `${name}${i}`), running, ...extra,
  });
  it('reads state and actions', () => {
    expect(projectState(p('a', 0, 2))).toBe('stopped');
    expect(projectState(p('a', 2, 2))).toBe('running');
    expect(projectState(p('a', 1, 2))).toBe('partial');
    expect(projectOps(p('a', 0, 2))).toEqual(['start']);
    expect(projectOps(p('a', 2, 2))).toEqual(['stop', 'restart']);
    expect(projectOps(p('a', 1, 2))).toEqual(['start', 'stop', 'restart']);
  });
  it('counts finished one-shot containers as done', () => {
    const init = c('init', 'exited', { id: 'x1' });
    const crashed = c('bad', 'exited', { id: 'x1', status: 'Exited (1) now' });
    const proj = p('x', 1, 2);
    expect(finished(proj, new Map([['x1', init]]))).toBe(1);
    expect(projectState(proj, new Map([['x1', init]]))).toBe('running');
    expect(projectState(proj, new Map([['x1', crashed]]))).toBe('partial');
    expect(projectState(p('x', 0, 2), new Map([['x1', init]]))).toBe('stopped');
  });
  it('searches and sorts', () => {
    const list = [p('zeta', 0, 1), p('alpha', 1, 1), p('beta', 1, 2, { services: ['db'], working_dir: '/home/me/beta' })];
    expect(projectView(list, '').map((x) => x.name)).toEqual(['beta', 'alpha', 'zeta']);
    expect(projectView(list, 'DB').map((x) => x.name)).toEqual(['beta']);
    expect(projectView(list, 'home/me').map((x) => x.name)).toEqual(['beta']);
  });
});

describe('cleanup', () => {
  it('totals reclaimable space', () => {
    expect(pruneTotal([
      { kind: 'containers', count: 1, reclaimable: 10, items: [] },
      { kind: 'build', count: 2, reclaimable: 5, items: [], error: 'x' },
      { kind: 'volumes', count: 1, reclaimable: -1, items: [] },
      { kind: 'images', count: 1, reclaimable: 30, items: [] },
    ])).toBe(40);
  });
});

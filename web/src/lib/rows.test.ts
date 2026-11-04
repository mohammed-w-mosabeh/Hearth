import { describe, expect, it } from 'vitest';
import { RowHistory, SPARK_S, addDockerStats, containerStatus, groupStatus, sparkPath, unitStatus } from './rows';

describe('row history', () => {
  it('keeps the last 10 minutes per row', () => {
    const h = new RowHistory();
    for (let t = 0; t <= 900; t += 2) h.add('a', t, t);
    const pts = h.get('a');
    expect(pts[0]?.[0]).toBe(900 - SPARK_S);
    expect(pts.at(-1)).toEqual([900, 900]);
    h.add('a', 900, 1);
    expect(h.get('a').at(-1)).toEqual([900, 900]);
  });

  it('forgets rows that went quiet', () => {
    const h = new RowHistory();
    h.add('old', 0, 1);
    h.add('new', 1000, 1);
    h.prune(1000);
    expect(Object.keys(h.snapshot())).toEqual(['new']);
  });
});

describe('docker stats', () => {
  const pt = (t: number, cpu: number) => ({ t, cpu, mem: 0, mem_limit: 0, net_rx: 0, net_tx: 0, blk_read: 0, blk_write: 0, pids: 0 });
  it('takes the history, then samples, and an empty history', () => {
    const h = new RowHistory();
    addDockerStats(h, { kind: 'history' });
    addDockerStats(h, { kind: 'history', history: { a: [pt(1, 5), pt(3, 6)] } });
    addDockerStats(h, { kind: 'sample', sample: { a: pt(5, 7), b: pt(5, 1) } });
    addDockerStats(h, { kind: 'sample' });
    expect(h.get('a')).toEqual([[1, 5], [3, 6], [5, 7]]);
    expect(h.get('b')).toEqual([[5, 1]]);
  });
});

describe('spark path', () => {
  it('needs two points', () => {
    expect(sparkPath([], 60, 16)).toBe('');
    expect(sparkPath([[1, 1]], 60, 16)).toBe('');
  });

  it('spans the window and breaks at gaps', () => {
    const d = sparkPath(
      [
        [0, 0],
        [10, 10],
        [500, 5],
        [540, 10],
        [600, 8],
      ],
      60,
      16,
    );
    expect(d.startsWith('M')).toBe(true);
    expect(d.match(/M/g)).toHaveLength(2); // 10 s to 500 s breaks; 40 and 60 s gaps are bridged
    // The highest value touches the top; 0 and 10 s share a bucket (5, mid-height).
    expect(d).toContain(' 1.0');
    expect(d).toBe('M1.0 8.0M51.0 8.0L55.0 1.0L59.0 3.8');
  });
});

describe('status shapes', () => {
  it('maps units', () => {
    expect(['failed', 'active', 'activating', 'inactive'].map(unitStatus)).toEqual(['failed', 'ok', 'degraded', 'stopped']);
  });
  it('maps containers', () => {
    expect(containerStatus('running')).toBe('ok');
    expect(containerStatus('failed')).toBe('failed');
    expect(containerStatus('paused')).toBe('degraded');
    expect(containerStatus('stopped')).toBe('stopped');
  });
  it('maps process groups', () => {
    expect(groupStatus({ procs: [] })).toBe('ok');
    expect(groupStatus({ procs: [{ pid: 1, start: 1, name: 'x', state: 'T', cpu: 0, rss: 0 }] })).toBe('degraded');
  });
});

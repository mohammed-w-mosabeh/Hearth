import { describe, expect, it } from 'vitest';
import { view } from './procs';
import type { ProcGroup } from './types';

const g = (id: string, kind: ProcGroup['kind'], cpu: number, mem: number, procs: string[] = [], read?: number): ProcGroup => ({
  id, kind, name: id.split(':')[1] ?? id, user: 'me', count: procs.length || 1, cpu, mem,
  ...(read === undefined ? {} : { read_bps: read }),
  procs: procs.map((name, i) => ({ pid: 100 + i, start: 1, name, state: 'S', cpu: 0, rss: 0 })),
});

const groups = [
  g('app:VSCode', 'app', 30, 4e9, ['code', 'code']),
  g('unit:cups.service', 'service', 1, 5e6, ['cupsd'], 10),
  g('container:abc', 'container', 5, 6e8),
  g('kernel', 'kernel', 0.5, 0),
];

describe('procs view', () => {
  it('sorts', () => {
    expect(view(groups, 'all', '', 'cpu', true).map((x) => x.id)).toEqual(['app:VSCode', 'container:abc', 'unit:cups.service', 'kernel']);
    expect(view(groups, 'all', '', 'mem', false)[0]?.id).toBe('kernel');
    // Unknown I/O sorts below zero.
    expect(view(groups, 'all', '', 'read', true)[0]?.id).toBe('unit:cups.service');
  });
  it('filters by kind and text', () => {
    expect(view(groups, 'services', '', 'cpu', true).map((x) => x.id)).toEqual(['unit:cups.service']);
    expect(view(groups, 'all', 'cupsd', 'cpu', true).map((x) => x.id)).toEqual(['unit:cups.service']);
    expect(view(groups, 'all', '101', 'cpu', true).map((x) => x.id)).toEqual(['app:VSCode']);
    expect(view(groups, 'other', '', 'cpu', true).map((x) => x.id)).toEqual(['kernel']);
  });
});

import { describe, expect, it } from 'vitest';
import { attachmentName, chartUnit, drawable, filterItems, kindLabel, reportQuery, trendColumns, xRange, type ItemTotal } from './reports';

const item = (id: string, name: string, kind: string): ItemTotal => ({
  item: id, kind, name, cpu_avg: 1, cpu_max: 2, busy_hours: 0, mem_avg: 0, mem_max: 0, hours: 1,
});

describe('reports', () => {
  it('maps report units to chart units', () => {
    expect(chartUnit('%')).toBe('pct');
    expect(chartUnit('bytes/s')).toBe('rate');
    expect(chartUnit('°C')).toBe('c');
    expect(chartUnit('')).toBe('load');
  });
  it('turns a trend into chart columns and a range', () => {
    expect(trendColumns({ metric: 'cpu', label: 'CPU', unit: '%', peak: 3, max: 100, t: [1, 2], v: [3, null] })).toEqual({
      t: [1, 2],
      series: { cpu: [3, null] },
    });
    expect(xRange('2026-10-29T00:00:00Z', '2026-10-29T00:01:00Z')).toEqual([1793232000, 1793232060]);
  });
  it('charts only trends with a line to draw', () => {
    const tr = { metric: 'cpu', label: 'CPU', unit: '%', peak: 3, max: 100, t: [1, 2, 3], v: [null, 3, null] };
    expect(drawable(tr)).toBe(false);
    expect(drawable({ ...tr, v: [1, null, 3] })).toBe(true);
  });
  it('builds queries with the item escaped', () => {
    expect(reportQuery('week')).toBe('period=week');
    expect(reportQuery('day', 'unit:a b&c.service', 'md')).toBe('period=day&id=unit%3Aa+b%26c.service&format=md');
  });
  it('names kinds and searches items', () => {
    expect(kindLabel('user-service')).toBe('reports.kind.userService');
    expect(kindLabel('exe')).toBe('reports.kind.other');
    const items = [item('unit:cups.service', 'cups', 'service'), item('app:org.gnome.Nautilus', 'Files', 'app')];
    expect(filterItems(items, 'nautilus').map((i) => i.name)).toEqual(['Files']);
    expect(filterItems(items, 'SERVICE cups').map((i) => i.name)).toEqual(['cups']);
  });
  it('accepts only plain attachment names', () => {
    expect(attachmentName('attachment; filename="hearth-week-2026-10-29.html"', 'x')).toBe('hearth-week-2026-10-29.html');
    expect(attachmentName('attachment; filename="../../.bashrc"', 'x')).toBe('x');
    expect(attachmentName(null, 'report.md')).toBe('report.md');
  });
});

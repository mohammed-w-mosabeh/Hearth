import { describe, expect, it } from 'vitest';
import { totalNet, Window, WINDOW_S } from './metrics';

describe('metrics window', () => {
  it('pushes, pads missing values and trims to the window', () => {
    const w = new Window();
    w.push(1000, new Map([['cpu.usage', 10]]));
    w.push(1001, new Map([['cpu.usage', 20], ['cpu.temp', 50]]));
    w.push(1002, new Map([['cpu.temp', 51]]));
    expect(w.data(['cpu.usage', 'cpu.temp', 'nope'])).toEqual([
      [1000, 1001, 1002],
      [10, 20, null],
      [null, 50, 51],
      [null, null, null],
    ]);
    w.push(1001 + WINDOW_S, new Map([['cpu.usage', 5]]));
    expect(w.t).toEqual([1001, 1002, 1001 + WINDOW_S]);
    expect(w.cols.get('cpu.usage')).toEqual([20, null, 5]);
  });

  it('prepends only server points older than what it has', () => {
    const w = new Window();
    w.push(1002, new Map([['cpu.usage', 30]]));
    w.prepend({ t: [1000_000, 1001_000, 1002_000], series: { 'cpu.usage': [10, 20, 99], 'mem.used': [1, 2, 3] } });
    expect(w.data(['cpu.usage', 'mem.used'])).toEqual([
      [1000, 1001, 1002],
      [10, 20, 30],
      [1, 2, null],
    ]);
  });

  it('totals network interfaces', () => {
    const got = totalNet({
      t: [1, 2, 3],
      series: { 'cpu.usage': [1, 2, 3], 'net.rx:eth0': [100, null, null], 'net.rx:wlp3s0': [null, 5, null] },
    });
    expect(got.series).toEqual({ 'cpu.usage': [1, 2, 3], 'net.rx': [100, 5, null] });
  });
});

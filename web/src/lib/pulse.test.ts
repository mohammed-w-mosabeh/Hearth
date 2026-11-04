import { describe, expect, it } from 'vitest';
import { PULSE_MS, emptyPulse, pulseFromSample, pulseFromSeries, pulseQuery } from './pulse';
import type { Sample } from './types';

describe('pulse', () => {
  it('asks only for a short stretch of the ring', () => {
    const q = new URLSearchParams(pulseQuery(100_000));
    expect(q.get('since')).toBe(String(100_000 - 2 * PULSE_MS));
    expect(q.getAll('m')).toContain('cpu.usage');
  });
  it('takes the newest value, the hottest sensor and sums interfaces', () => {
    const p = pulseFromSeries({
      t: [1, 2],
      series: {
        'cpu.usage': [10, null],
        'mem.used': [5, 6],
        'cpu.temp': [40, 50],
        'temp:a/b': [70, 60],
        'net.rx:eth0': [1, 2],
        'net.rx:wlan0': [3, 4],
        'net.tx:eth0': [1, 1],
        'battery.pct:BAT0': [80, 79],
        'battery.pct:BAT1': [20, 19],
      },
    });
    expect(p).toEqual({ cpu: 10, memUsed: 6, temp: 60, down: 6, up: 1, battery: 79 });
  });
  it('leaves what is missing empty', () => {
    expect(pulseFromSeries({ t: [], series: {} })).toEqual(emptyPulse);
  });
  it('reads a sample the same way, without virtual interfaces', () => {
    const s = {
      cpu: { usage: 5 },
      mem: { used: 9 },
      temps: [{ c: 30 }],
      cpu_temp_c: 44,
      net: [
        { rx_bps: 1, tx_bps: 2, virtual: false },
        { rx_bps: 100, tx_bps: 100, virtual: true },
      ],
      power: { batteries: [] },
    } as unknown as Sample;
    expect(pulseFromSample(s)).toEqual({ cpu: 5, memUsed: 9, temp: 44, down: 1, up: 2, battery: null });
  });
});

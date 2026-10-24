import { describe, expect, it } from 'vitest';
import { allSelectors, format, label } from './charts';

describe('charts', () => {
  it('asks for at most 32 selectors', () => {
    expect(allSelectors().length).toBeLessThanOrEqual(32);
  });
  it('labels metrics', () => {
    expect(label('cpu.usage')).toBe('Total');
    expect(label('cpu.core.11')).toBe('Core 11');
    expect(label('net.rx:wlp3s0')).toBe('wlp3s0 ↓');
    expect(label('disk.write:sda')).toBe('sda write');
    expect(label('disk.used:/home')).toBe('/home');
    expect(label('temp:coretemp/Core 0')).toBe('coretemp Core 0');
    expect(label('gpu.util:0000:01:00.0', { '0000:01:00.0': 'Radeon' })).toBe('Radeon');
    expect(label('gpu.util:0000:02:00.0')).toBe('0000:02:00.0');
    expect(label('battery.pct:BAT1')).toBe('BAT1');
  });
  it('formats by unit', () => {
    expect(format('mhz', 2400)).toBe('2.40 GHz');
    expect(format('load', 1.234)).toBe('1.23');
    expect(format('w', 4.56)).toBe('4.6 W');
  });
});

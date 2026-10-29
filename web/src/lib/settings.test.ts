import { describe, expect, it } from 'vitest';
import { bars, otherConnections, savedFor, validHostname, volumeParam, zoneGroups, type Connection } from './settings';

const conns: Connection[] = [
  { uuid: '1', name: 'Home', type: 'wifi', ssid: 'Home', active: true, autoconnect: true },
  { uuid: '2', name: 'Office VPN', type: 'vpn', active: false, autoconnect: false },
];

describe('settings helpers', () => {
  it('counts bars', () => {
    expect([0, 24, 25, 60, 99].map(bars)).toEqual([1, 1, 2, 3, 4]);
  });
  it('finds saved networks and the rest', () => {
    expect(savedFor(conns, 'Home')?.uuid).toBe('1');
    expect(savedFor(conns, 'Cafe')).toBeUndefined();
    expect(otherConnections(conns).map((c) => c.uuid)).toEqual(['2']);
  });
  it('formats volumes for wpctl', () => {
    expect(volumeParam(75)).toBe('0.75');
    expect(volumeParam(200)).toBe('1.50');
    expect(volumeParam(-3)).toBe('0.00');
    expect(volumeParam(33.6)).toBe('0.34');
  });
  it('checks hostnames like hostnamed', () => {
    expect(validHostname('box-2.lan')).toBe(true);
    expect(validHostname('Box')).toBe(false);
    expect(validHostname('-x')).toBe(false);
    expect(validHostname('a..b')).toBe(false);
  });
  it('groups zones by region', () => {
    expect(zoneGroups(['Africa/Cairo', 'Asia/Amman', 'Asia/Tokyo', 'UTC'])).toEqual([
      { region: 'Africa', zones: ['Africa/Cairo'] },
      { region: 'Asia', zones: ['Asia/Amman', 'Asia/Tokyo'] },
      { region: '', zones: ['UTC'] },
    ]);
  });
});

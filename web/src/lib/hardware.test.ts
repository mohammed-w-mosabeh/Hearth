import { describe, expect, it } from 'vitest';
import { deviceName, updatable } from './hardware';

describe('hardware', () => {
  it('names a device', () => {
    expect(deviceName({ id: '1:2', name: 'Mouse', vendor: 'Acme' })).toBe('Mouse');
    expect(deviceName({ id: '1:2', vendor: 'Acme' })).toBe('Acme');
    expect(deviceName({ id: '1:2' })).toBe('1:2');
  });
  it('counts real updates', () => {
    expect(updatable([{ id: 'a', name: 'a', version: '1', update: '2' }, { id: 'b', name: 'b', version: '1', update: '1' }, { id: 'c', name: 'c' }])).toBe(1);
  });
});

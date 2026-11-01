import { describe, expect, it } from 'vitest';
import { markerText, nearest } from './markers';

describe('markers', () => {
  it('says what happened', () => {
    expect(markerText({ t: 0, kind: 'boot' })).toBe('Computer started');
    expect(markerText({ t: 0, kind: 'upgrade', count: 12 })).toBe('12 packages upgraded');
    expect(markerText({ t: 0, kind: 'upgrade', subject: 'curl', count: 1 })).toBe('curl upgraded');
    expect(markerText({ t: 0, kind: 'alert', subject: 'CPU busy' })).toBe('Alert: CPU busy');
    expect(markerText({ t: 0, kind: 'container', subject: 'web' })).toBe('Container restarted: web');
  });

  it('finds the nearest marker within reach', () => {
    const items = [{ x: 10 }, { x: 20 }];
    expect(nearest(items, 18, 6)).toBe(items[1]);
    expect(nearest(items, 30, 6)).toBeNull();
    expect(nearest([], 0, 6)).toBeNull();
  });
});

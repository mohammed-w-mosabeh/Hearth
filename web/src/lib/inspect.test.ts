import { describe, expect, it } from 'vitest';
import { KEEP, formatInspect, parseInspect, push, series, type Inspect } from './inspect';

describe('inspect items', () => {
  it('round-trips every kind', () => {
    const items: Inspect[] = [
      { kind: 'unit', scope: 'system', name: 'getty@tty1.service' },
      { kind: 'unit', scope: 'user', name: 'pipewire.service' },
      { kind: 'container', id: 'a1b2c3d4e5f6' },
      { kind: 'package', name: 'libc6:amd64' },
      { kind: 'port', proto: 'udp', port: 5353 },
      { kind: 'proc', group: 'app:com.example.App' },
      { kind: 'alert', id: 42 },
    ];
    for (const it of items) expect(parseInspect(formatInspect(it))).toEqual(it);
  });

  it('refuses what is not an item', () => {
    for (const bad of [
      '',
      'unit',
      'unit:',
      'unit:root/x',
      'unit:system/a/b',
      'container:-x',
      'container:a b',
      'package:Foo',
      'port:tcp/0',
      'port:tcp/65536',
      'port:icmp/1',
      'alert:0',
      'alert:x',
      'file:/etc/passwd',
      'proc:a\nb',
      `proc:${'x'.repeat(600)}`,
    ]) {
      expect(parseInspect(bad), bad).toBeNull();
    }
  });

  it('keeps the newest points for a mini chart', () => {
    let p: [number, number][] = [];
    for (let i = 0; i < KEEP + 10; i++) p = push(p, i, i * 2);
    expect(p).toHaveLength(KEEP);
    expect(p[0]).toEqual([10, 20]);
    expect(push(p, 5, 1)).toBe(p);
    expect(
      series([
        [1, 2],
        [3, 4],
      ]),
    ).toEqual([
      [1, 3],
      [2, 4],
    ]);
  });
});

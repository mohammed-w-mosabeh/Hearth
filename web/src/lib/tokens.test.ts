import { describe, expect, it } from 'vitest';
import { scrapeConfig, validName } from './tokens';

describe('validName', () => {
  it('takes what the server takes', () => {
    for (const ok of ['cli', 'a', 'prom-1', '0', 'a'.repeat(32)]) expect(validName(ok)).toBe(true);
    for (const bad of ['', '-a', 'A', 'a b', 'a/b', '../x', 'a.b', 'a'.repeat(33)]) expect(validName(bad)).toBe(false);
  });
});

describe('scrapeConfig', () => {
  it('points at the loopback port and the file, never a token', () => {
    const out = scrapeConfig(7380, '/home/me/.config/hearth/tokens/prom.token');
    expect(out).toContain("targets: ['127.0.0.1:7380']");
    expect(out).toContain('credentials_file: /home/me/.config/hearth/tokens/prom.token');
  });
});

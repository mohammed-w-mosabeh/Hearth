import { describe, expect, it } from 'vitest';
import { emptyRule, ruleParams, sourceLooksValid, validPort } from './firewall';

describe('validPort', () => {
  it('takes 1 to 65535', () => {
    expect(validPort(1)).toBe(true);
    expect(validPort(65535)).toBe(true);
    expect(validPort(0)).toBe(false);
    expect(validPort(65536)).toBe(false);
    expect(validPort(1.5)).toBe(false);
    expect(validPort(null)).toBe(false);
  });
});

describe('sourceLooksValid', () => {
  it('takes empty, addresses and CIDRs', () => {
    for (const s of ['', '10.0.0.1', '10.0.0.0/8', '2001:db8::/32', ' 10.0.0.1 ']) expect(sourceLooksValid(s)).toBe(true);
  });
  it('refuses words', () => {
    for (const s of ['any', '10.0.0.0/8 port 22', '<b>', '--force']) expect(sourceLooksValid(s)).toBe(false);
  });
});

describe('ruleParams', () => {
  it('sends strings, trimmed', () => {
    expect(ruleParams({ ...emptyRule(), port: 443, source: ' 10.0.0.0/8 ' })).toEqual({ action: 'allow', proto: 'tcp', port: '443', source: '10.0.0.0/8' });
  });
  it('sends a rule with no source as anywhere', () => {
    expect(ruleParams({ action: 'deny', proto: 'udp', port: 53 }).source).toBe('');
  });
});

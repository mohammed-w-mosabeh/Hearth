import { describe, expect, it } from 'vitest';
import en from './i18n/en.json';
import { causeKey, parseTargets, targetsText } from './connectivity';

describe('connectivity', () => {
  it('reads one target a line', () => {
    expect(parseTargets(' example.com:443 \n\n9.9.9.9\n[2001:db8::1]:53\n2001:db8::2\n')).toEqual([
      { host: 'example.com', port: 443 },
      { host: '9.9.9.9' },
      { host: '2001:db8::1', port: 53 },
      { host: '2001:db8::2' },
    ]);
  });
  it('round-trips the text', () => {
    const text = 'example.com:443\n9.9.9.9\n[2001:db8::1]:53';
    expect(targetsText(parseTargets(text))).toBe(text);
  });
  it('explains every cause', () => {
    for (const c of ['gateway', 'dns', 'upstream', 'future']) expect(en).toHaveProperty([causeKey(c)]);
  });
});

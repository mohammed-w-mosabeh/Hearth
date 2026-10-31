import { describe, expect, it } from 'vitest';
import en from './i18n/en.json';
import { CHECKS, FINDINGS, checkHint, noneKey, statusLabel, verdictKey, checkTitle, cveRows, findingKey, overall, riskKey, trackerURL, type CVEs, type Report, restartKey } from './security';

const report = (statuses: Report['checks'][number]['status'][]): Report => ({
  time: 0,
  checks: statuses.map((status, i) => ({ id: `c${i}`, status, findings: [] })),
  cves: { installed: false, list: [], fixed: 0, remote: 0 },
  counts: {},
});

describe('security', () => {
  it('names checks and findings', () => {
    expect(checkTitle('ssh')).toBe('security.check.ssh');
    expect(checkTitle('future')).toBe('security.check.other');
    expect(checkHint('future')).toBeNull();
    expect(findingKey('ssh.root_login')).toBe('security.f.ssh_root_login');
    expect(findingKey('path.world_file')).toBe('security.f.path_world_file');
    expect(findingKey('new.thing')).toBeNull();
  });
  it('has a message for every key it builds', () => {
    const keys: string[] = [...[...FINDINGS].map((c) => findingKey(c) ?? ''), 'security.check.other', 'security.none.other'];
    for (const id of CHECKS) keys.push(checkTitle(id), checkHint(id) ?? '', noneKey(id));
    for (const s of ['ok', 'warning', 'critical', 'unknown', 'off'] as const) keys.push(statusLabel(s), verdictKey(s));
    for (const r of ['privileged', 'cap-add', 'host-pid', 'host-network', 'host-ipc', 'host-mount:/', 'x']) keys.push(riskKey(r).key);
    const missing = keys.filter((k) => !(k in en));
    expect(missing).toEqual([]);
  });
  it('reads container risks', () => {
    expect(riskKey('privileged')).toEqual({ key: 'security.risk.privileged' });
    expect(riskKey('host-mount:/var/run/docker.sock')).toEqual({ key: 'security.risk.host-mount', path: '/var/run/docker.sock' });
    expect(riskKey('<b>')).toEqual({ key: 'security.risk.other' });
  });
  it('takes the worst status', () => {
    expect(overall(report(['ok', 'off', 'warning']))).toBe('warning');
    expect(overall(report(['ok', 'critical', 'unknown']))).toBe('critical');
    expect(overall(report(['off']))).toBe('ok');
  });
  it('lists CVEs and links only real IDs', () => {
    const v: CVEs = {
      installed: true, fixed: 1, remote: 0,
      list: [{ id: 'CVE-2024-0001', package: 'a', fixed: true, remote: false }, { id: 'CVE-2024-0002', package: 'b', fixed: false, remote: false }],
    };
    expect(cveRows(v, true).map((c) => c.id)).toEqual(['CVE-2024-0001']);
    expect(cveRows(v, false, 1)).toHaveLength(1);
    expect(trackerURL('CVE-2024-12345')).toBe('https://security-tracker.debian.org/tracker/CVE-2024-12345');
    expect(trackerURL('TEMP-0000000-ABCDEF')).not.toBeNull();
    expect(trackerURL('javascript:alert(1)')).toBeNull();
  });
});

describe('restart reminder', () => {
  it('stays quiet when nothing waits', () => {
    expect(restartKey({ reboot: false, services: [] })).toBeNull();
  });
  it('puts a reboot before services', () => {
    expect(restartKey({ reboot: true, services: ['a.service'] })).toBe('home.restart.reboot');
    expect(restartKey({ reboot: false, services: ['a.service'] })).toBe('home.restart.services');
  });
});

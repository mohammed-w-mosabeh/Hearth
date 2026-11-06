import { describe, expect, it } from 'vitest';
import type { AlertEvent } from './alerts';
import { MAX_SNOOZED, RULES, SNOOZE_MS, collect, queue, readSnoozed, score, snooze, unchecked, type Item } from './health';
import type { Report } from './security';
import type { Unit } from './services';

const unit = (name: string, active: string): Unit => ({
  name,
  scope: 'system',
  load: 'loaded',
  active,
  sub: active,
});

const alert = (id: number, severity: AlertEvent['severity'], ended?: number): AlertEvent => ({
  id,
  rule_id: 1,
  rule_name: 'Hot',
  metric: 'temp',
  instance: 'cpu',
  severity,
  op: '>',
  threshold: 80,
  value: 90,
  peak: 91,
  started_at: 1,
  ...(ended === undefined ? {} : { ended_at: ended }),
  notified: false,
});

const security = (
  findings: {
    id: string;
    code: string;
    severity?: 'info' | 'warning' | 'critical';
    subject?: string;
  }[],
): Report => ({
  time: 1,
  counts: {},
  cves: { installed: false, list: [], fixed: 0, remote: 0 },
  checks: [...new Set(findings.map((f) => f.id))].map((id) => ({
    id,
    status: 'warning',
    findings: findings
      .filter((f) => f.id === id)
      .map((f) => ({
        code: f.code,
        severity: f.severity ?? 'warning',
        ...(f.subject === undefined ? {} : { subject: f.subject }),
      })),
  })),
});

const item = (id: string, source: Item['source']): Item => ({
  id,
  source,
  subject: id,
  action: { kind: 'open', route: 'home' },
});

describe('health', () => {
  it('is 100 with nothing to report', () => {
    expect(score(collect({ units: [], alerts: [] }))).toEqual({
      score: 100,
      deductions: [],
    });
  });

  it('collects every source', () => {
    const items = collect({
      units: [unit('a.service', 'failed'), unit('b.service', 'active')],
      alerts: [alert(1, 'critical'), alert(2, 'warning', 5), alert(3, 'info')],
      security: security([
        {
          id: 'updates',
          code: 'updates.security',
          severity: 'critical',
          subject: '4',
        },
        {
          id: 'updates',
          code: 'updates.other',
          severity: 'info',
          subject: '9',
        },
        { id: 'listeners', code: 'listener.exposed', subject: 'tcp 22' },
        { id: 'restart', code: 'restart.services', severity: 'info' },
      ]),
      forecasts: {
        disks: [
          { mount: '/', days: 3, per_day: 1, window_days: 7 },
          { mount: '/home', days: 30, per_day: 1, window_days: 7 },
        ],
        batteries: {},
      },
      smart: {
        status: 'ok',
        disks: [
          {
            device: 'sda',
            model: 'X',
            supported: true,
            failing: true,
            testing: false,
          },
        ],
      },
      lynis: {
        installed: true,
        schedule: 'off',
        running: false,
        history: [
          { time: 9, index: 60, warnings: 0, suggestions: 0 },
          { time: 8, index: 70, warnings: 0, suggestions: 0 },
        ],
      },
    });
    expect(items.map((i) => [i.id, i.source, i.subject])).toEqual([
      ['unit:system/a.service', 'unit', 'a.service'],
      ['alert:1', 'alert.critical', 'Hot (cpu)'],
      ['alert:3', 'alert.info', 'Hot (cpu)'],
      ['updates.security', 'updates', '4'],
      ['listener:tcp 22', 'listener', 'tcp 22'],
      ['restart', 'restart', 'services'],
      ['forecast:/', 'forecast', '/'],
      ['smart:sda', 'smart', 'sda (X)'],
      ['lynis:9', 'lynis', '70 → 60'],
    ]);
    expect(items[0]?.action).toEqual({
      kind: 'unit',
      unit: unit('a.service', 'failed'),
    });
    // 10 + 15 + 0 + 15 + 5 + 5 + 15 + 25 + 10
    expect(score(items).score).toBe(0);
  });

  it('does not count a listener behind a firewall', () => {
    expect(
      collect({
        security: security([
          {
            id: 'listeners',
            code: 'listener.exposed',
            severity: 'info',
            subject: 'tcp 22',
          },
        ]),
      }),
    ).toEqual([]);
  });

  it('caps each source and never goes below 0', () => {
    const units = Array.from({ length: 9 }, (_, i) => item(`unit:${i}`, 'unit'));
    const r = score(units);
    expect(r).toEqual({
      score: 100 - RULES.unit.cap,
      deductions: [{ source: 'unit', count: 9, points: RULES.unit.cap }],
    });
    const smart = Array.from({ length: 9 }, (_, i) => item(`smart:${i}`, 'smart'));
    expect(score([...units, ...smart, item('u', 'updates'), item('f', 'forecast'), item('g', 'forecast')]).score).toBe(0);
  });

  it('counts unhealthy apps, failed backups and found updates', () => {
    const last = (ok: boolean) => ({ at: '2026-11-06T03:00:00Z', scheduled: true, ok });
    const items = collect({
      apps: [
        { name: 'cloud', state: 'partial', unhealthy: ['cloud-db-1'], backup: last(false), updates: ['web'], scheduled: true },
        { name: 'wiki', state: 'running', unhealthy: [], backup: last(true), updates: [], scheduled: false },
        { name: 'new', state: 'stopped', unhealthy: [], backup: null, updates: [], scheduled: false },
      ],
    });
    expect(items.map((i) => [i.id, i.source, i.subject])).toEqual([
      ['app:cloud', 'app', 'cloud (cloud-db-1)'],
      ['backup:cloud:2026-11-06T03:00:00Z', 'backup', 'cloud'],
      ['appUpdate:cloud', 'appUpdate', 'cloud (web)'],
    ]);
    expect(items.every((i) => i.action.kind === 'open' && i.action.route === 'stacks')).toBe(true);
    // 10 + 15 + 5
    expect(score(items).score).toBe(70);
    expect(unchecked({ apps: null })).toEqual(['apps']);
  });

  it('lists what it could not read', () => {
    expect(unchecked({ units: null, alerts: [], security: undefined })).toEqual(['units']);
  });

  it('a rising hardening index deducts nothing', () => {
    const history = [
      { time: 9, index: 70, warnings: 0, suggestions: 0 },
      { time: 8, index: 60, warnings: 0, suggestions: 0 },
    ];
    expect(
      collect({
        lynis: { installed: true, schedule: 'off', running: false, history },
      }),
    ).toEqual([]);
  });
});

describe('needs attention', () => {
  const now = 1_000_000;

  it('puts the most serious first and hides what is snoozed', () => {
    const items = [item('r', 'restart'), item('l', 'listener'), item('u', 'unit'), item('v', 'unit')];
    expect(queue(items, {}, now).map((i) => i.id)).toEqual(['u', 'v', 'l', 'r']);
    expect(queue(items, { u: now + 1, l: now - 1 }, now).map((i) => i.id)).toEqual(['v', 'l', 'r']);
  });

  it('snoozes for 24 h and drops what has ended', () => {
    expect(snooze({ old: now - 1, kept: now + 5 }, 'x', now)).toEqual({
      kept: now + 5,
      x: now + SNOOZE_MS,
    });
  });

  it('keeps at most the allowed number', () => {
    let s = {};
    for (let i = 0; i < MAX_SNOOZED + 5; i++) s = snooze(s, `id${i}`, now + i);
    const ids = Object.keys(s);
    expect(ids).toHaveLength(MAX_SNOOZED);
    expect(ids).not.toContain('id0');
    expect(ids).toContain(`id${MAX_SNOOZED + 4}`);
  });

  it('reads only a well-formed stored value', () => {
    expect(readSnoozed(null, now)).toEqual({});
    expect(readSnoozed({ until: [1] }, now)).toEqual({});
    expect(readSnoozed({ until: { a: now + 1, b: 'x', c: 1.5, d: now - 1 } }, now)).toEqual({ a: now + 1 });
  });
});

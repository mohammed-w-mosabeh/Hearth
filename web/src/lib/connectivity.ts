// Connectivity (ROADMAP 6.5): GET /api/v1/connectivity, internal/connectivity.

import type { MessageKey } from './i18n';

export interface Target {
  host: string;
  port?: number;
}

export interface ConnConfig {
  enabled: boolean;
  dns_name?: string;
  targets: Target[];
  cap_gib?: number;
  cap_iface?: string;
}

export interface CheckResult {
  name: string;
  kind: 'gateway' | 'dns' | 'target' | string;
  ok: boolean;
  ms?: number;
  skipped?: boolean;
}

export interface Outage {
  id: number;
  started_at: number;
  ended_at?: number;
  cause: string;
}

export interface IfaceUsage {
  iface: string;
  today_rx: number;
  today_tx: number;
  month_rx: number;
  month_tx: number;
}

export interface ConnReport {
  config: ConnConfig;
  checks: CheckResult[];
  down: boolean;
  outages: Outage[];
  usage: IfaceUsage[];
}

const CAUSES: Record<string, MessageKey> = {
  gateway: 'conn.cause.gateway',
  dns: 'conn.cause.dns',
  upstream: 'conn.cause.upstream',
};

export function causeKey(cause: string): MessageKey {
  return CAUSES[cause] ?? 'conn.cause.upstream';
}

/** One target a line: "host:port", "[v6]:port", or a host alone for a ping. */
export function parseTargets(text: string): Target[] {
  const out: Target[] = [];
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (line === '') continue;
    const v6 = /^\[(.+)\]:(\d+)$/.exec(line);
    const named = /^([^:]+):(\d+)$/.exec(line);
    const m = v6 ?? named;
    out.push(m?.[1] !== undefined ? { host: m[1], port: Number(m[2]) } : { host: line });
  }
  return out;
}

export function targetsText(targets: readonly Target[]): string {
  return targets.map((t) => (t.port ? (t.host.includes(':') ? `[${t.host}]:${t.port}` : `${t.host}:${t.port}`) : t.host)).join('\n');
}

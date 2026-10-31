// Security page: types of internal/posture and helpers to show them.

import type { MessageKey } from './i18n';

export type Severity = 'info' | 'warning' | 'critical';
export type Status = 'ok' | 'warning' | 'critical' | 'unknown' | 'off';

export interface Finding {
  code: string;
  severity: Severity;
  subject?: string;
  details?: string[];
}

export interface Check {
  id: string;
  status: Status;
  findings: Finding[];
  error?: string;
}

export interface CVE {
  id: string;
  package: string;
  fixed: boolean;
  remote: boolean;
  urgency?: string;
}

export interface CVEs {
  installed: boolean;
  suite?: string;
  time?: number;
  error?: string;
  list: CVE[];
  fixed: number;
  remote: number;
  truncated?: boolean;
}

export interface SSH {
  installed: boolean;
  active: boolean;
  permit_root_login: string;
  password_authentication: string;
  kbd_interactive_authentication: string;
  permit_empty_passwords: string;
  ports: string[];
  listen_addresses: string[];
  unreadable?: string[];
  match?: boolean;
}

export interface Report {
  time: number;
  checks: Check[];
  ssh?: SSH;
  cves: CVEs;
  counts: Partial<Record<Status, number>>;
}

export const CHECKS = ['listeners', 'firewall', 'ssh', 'updates', 'unattended', 'restart', 'cves', 'containers', 'path'] as const;
type CheckId = (typeof CHECKS)[number];

const known = (id: string): id is CheckId => (CHECKS as readonly string[]).includes(id);

export function checkTitle(id: string): MessageKey {
  return known(id) ? (`security.check.${id}` as MessageKey) : 'security.check.other';
}

export function checkHint(id: string): MessageKey | null {
  return known(id) ? (`security.hint.${id}` as MessageKey) : null;
}

/** "Nothing found" for a check that is ok. */
export function noneKey(id: string): MessageKey {
  return known(id) ? (`security.none.${id}` as MessageKey) : 'security.none.other';
}

export function verdictKey(s: Status): MessageKey {
  return `security.verdict.${s}` as MessageKey;
}

export function statusLabel(s: Status): MessageKey {
  return `security.status.${s}` as MessageKey;
}

export const FINDINGS = new Set([
  'firewall.active', 'firewall.inactive', 'firewall.none', 'listener.exposed',
  'ssh.active', 'ssh.inactive', 'ssh.root_login', 'ssh.empty_passwords', 'ssh.password_auth', 'ssh.kbd_interactive', 'ssh.unreadable', 'ssh.match',
  'updates.security', 'updates.other',
  'unattended.active', 'unattended.never', 'unattended.stale', 'unattended.off', 'unattended.no_lists', 'unattended.no_timer', 'unattended.no_tool',
  'restart.reboot', 'restart.kernel', 'restart.services', 'restart.sessions', 'restart.no_tool', 'restart.scan_failed', 'restart.scanning',
  'cves.not_installed', 'cves.not_run', 'cves.fixed', 'cves.open',
  'container.risky', 'container.risky_stopped',
  'path.world', 'path.world_file', 'path.owner', 'path.owner_file', 'path.relative', 'path.truncated',
]);

/** The message of a finding; an unknown code (a newer hearthd) shows as itself. */
export function findingKey(code: string): MessageKey | null {
  return FINDINGS.has(code) ? (`security.f.${code.replace('.', '_')}` as MessageKey) : null;
}

/** Container risk codes from the broker, as message keys. */
export function riskKey(risk: string): { key: MessageKey; path?: string } {
  if (risk.startsWith('host-mount:')) return { key: 'security.risk.host-mount', path: risk.slice('host-mount:'.length) };
  const k = ['privileged', 'cap-add', 'host-pid', 'host-network', 'host-ipc'].includes(risk) ? risk : 'other';
  return { key: `security.risk.${k}` as MessageKey };
}

/** GET /api/v1/restart. */
export interface Restart {
  reboot: boolean;
  services: string[];
}

/** Home's reminder for r, null when nothing waits. */
export function restartKey(r: Restart): MessageKey | null {
  if (r.reboot) return 'home.restart.reboot';
  return r.services.length > 0 ? 'home.restart.services' : null;
}

/** The overall verdict: the worst status among the checks that ran. */
export function overall(r: Report): Status {
  const order: Status[] = ['critical', 'warning', 'unknown', 'ok'];
  for (const s of order) if (r.checks.some((c) => c.status === s)) return s;
  return 'ok';
}

/** CVEs to list: fixed ones (actionable) first, at most limit. */
export function cveRows(v: CVEs, onlyFixed: boolean, limit = 200): CVE[] {
  return v.list.filter((c) => !onlyFixed || c.fixed).slice(0, limit);
}

/** A tracker link for a CVE ID (plain text when it is not one). */
export function trackerURL(id: string): string | null {
  return /^(CVE-\d{4}-\d{4,}|TEMP-\d{7}-[0-9A-F]{6})$/.test(id) ? `https://security-tracker.debian.org/tracker/${id}` : null;
}

// Firewall section of the Network page: types of hearth-broker's ufw.state,
// and the checks the form makes before asking for a preview.

export interface FirewallRule {
  action: string;
  proto: string;
  port?: number;
  source?: string;
  family: 'ipv4' | 'ipv6' | 'both';
  editable: boolean;
  text?: string;
}

export interface FirewallState {
  installed: boolean;
  active: boolean;
  incoming?: string;
  rules: FirewallRule[];
  truncated?: boolean;
}

export interface RuleForm {
  action: 'allow' | 'deny';
  proto: 'tcp' | 'udp';
  port: number | null;
  source: string;
}

export const emptyRule = (): RuleForm => ({ action: 'allow', proto: 'tcp', port: null, source: '' });

/** Mirrors broker.CheckUfwRule's port. */
export function validPort(n: number | null): boolean {
  return n !== null && Number.isInteger(n) && n >= 1 && n <= 65535;
}

/** A rough check for the form: the broker has the last word on a source. */
export function sourceLooksValid(s: string): boolean {
  const v = s.trim();
  return v === '' || /^[0-9a-fA-F:.]+(\/[0-9]{1,3})?$/.test(v);
}

/** The params of a rule, for both add and delete. */
export function ruleParams(r: { action: string; proto: string; port?: number | null; source?: string }): Record<string, string> {
  return { action: r.action, proto: r.proto, port: String(r.port ?? ''), source: (r.source ?? '').trim() };
}

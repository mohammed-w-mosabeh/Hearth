// Client for POST /api/v1/actions/{kind}/{action} (internal/actions).

import { api } from './api';
import type { MessageKey } from './i18n';

export type Risk = 'safe' | 'reversible' | 'destructive';

export interface Item {
  id: string;
  name: string;
  detail?: string;
}

export interface Blocked extends Item {
  reason: string;
}

export interface Plan {
  summary: string;
  items: Item[];
  blocked: Blocked[];
  confirm?: string;
  risk: Risk;
  token?: string;
  expires?: string;
}

export interface Result {
  done: number;
  failed: Blocked[];
  blocked: Blocked[];
}

export type Params = Record<string, string>;

/** One field of CapDialog. */
export interface CapField {
  key: string;
  label: MessageKey;
  options?: string[];
  none?: MessageKey;
}

function post<T>(kind: string, action: string, body: object): Promise<T> {
  return api<T>(`/actions/${encodeURIComponent(kind)}/${encodeURIComponent(action)}`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

export function preview(kind: string, action: string, target: string, params: Params = {}): Promise<Plan> {
  return post<Plan>(kind, action, { target, params, preview: true });
}

export function run(kind: string, action: string, target: string, params: Params = {}, token?: string, confirm?: string): Promise<Result> {
  return post<Result>(kind, action, { target, params, ...(token ? { token } : {}), ...(confirm ? { confirm } : {}) });
}

const reasons: Record<string, MessageKey> = {
  'other-user': 'reason.otherUser',
  system: 'reason.system',
  self: 'reason.self',
  critical: 'reason.critical',
  session: 'reason.session',
  browser: 'reason.browser',
  gone: 'reason.gone',
  changed: 'reason.changed',
  refused: 'reason.refused',
  denied: 'reason.denied',
  error: 'reason.error',
  unauthorized: 'reason.unauthorized',
  'not-found': 'reason.notFound',
  masked: 'reason.masked',
  'no-install': 'reason.noInstall',
  'job-failed': 'reason.jobFailed',
  'in-use': 'reason.inUse',
  essential: 'reason.essential',
  protected: 'reason.protected',
  required: 'reason.required',
  manager: 'reason.manager',
  root: 'reason.root',
  pip: 'reason.pip',
  unavailable: 'reason.unavailable',
  enterprise: 'reason.enterprise',
};

/** The i18n key explaining why an item was blocked or failed. */
export function reasonKey(code: string): MessageKey {
  return reasons[code] ?? 'reason.error';
}

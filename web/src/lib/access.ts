// Access section of the Security page: types of GET /security/access.

export interface AccessSource {
  source: string;
  accepted: number;
  failed: number;
  last: number;
}

export interface AccessDay {
  day: string;
  accepted: number;
  failed: number;
}

export interface AccessSession {
  id: string;
  user: string;
  uid: number;
  seat?: string;
  tty?: string;
  type?: string;
  service?: string;
  state?: string;
  remote: boolean;
  remoteHost?: string;
  since?: number;
}

export interface AccessUser {
  name: string;
  uid: number;
  login: boolean;
  groups: string[];
  sudo?: boolean;
  docker?: boolean;
}

export interface AccessReport {
  days: number;
  limited?: boolean;
  truncated?: boolean;
  sources: AccessSource[];
  more?: number;
  byDay: AccessDay[];
  sessions: AccessSession[];
  sessionsError?: boolean;
  users: AccessUser[];
  usersError?: boolean;
}

// Where a session is: the remote host, else its terminal or seat. null means
// this computer, which the page names in the viewer's language.
export function sessionPlace(s: AccessSession): string | null {
  if (s.remote) return s.remoteHost || s.tty || s.service || '?';
  return s.tty || s.seat || null;
}

// Log explorer (ROADMAP 6.6): GET /api/v1/logs, internal/logview.

export interface LogLine {
  t?: number;
  level: 'error' | 'warn' | 'info' | 'debug' | string;
  text: string;
  count?: number;
}

export interface LogSource {
  id: string;
  lines: LogLine[];
  read: number;
  truncated?: boolean;
  error?: string;
}

export const MAX_SOURCES = 4;

export function logsQuery(sources: readonly string[], pattern: string): string {
  const q = new URLSearchParams();
  for (const s of sources) q.append('s', s);
  if (pattern) q.set('re', pattern);
  return q.toString();
}

/** The name a source goes by: a container's, or the unit. */
export function sourceName(id: string): string {
  return id.startsWith('container:') ? id.slice('container:'.length) : id;
}

/** The list with one more source, if there is room and it is new. */
export function added(list: readonly string[], id: string): string[] {
  return id === '' || list.includes(id) || list.length >= MAX_SOURCES ? [...list] : [...list, id];
}

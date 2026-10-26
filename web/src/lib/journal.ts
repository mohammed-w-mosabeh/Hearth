// Journal viewer helpers (GET /api/v1/services/journal).

export interface JournalEntry {
  cursor: string;
  t: number; // Unix ms
  prio: number; // 0 emerg .. 7 debug
  ident?: string;
  pid?: number;
  msg: string;
}

export interface Journal {
  entries: JournalEntry[];
  cursor?: string;
  limited?: boolean;
}

/** Most entries kept in the viewer; older ones are dropped. */
export const MAX_ENTRIES = 2000;

/** Appends newer entries, skipping any already shown, keeping the newest MAX_ENTRIES. */
export function appendEntries(have: JournalEntry[], more: JournalEntry[]): JournalEntry[] {
  if (more.length === 0) return have;
  const seen = new Set(have.map((e) => e.cursor));
  const out = have.concat(more.filter((e) => !seen.has(e.cursor)));
  return out.length > MAX_ENTRIES ? out.slice(out.length - MAX_ENTRIES) : out;
}

/** Whether an entry passes the text filter and the "errors and warnings" switch (priority 4 or more severe). */
export function matches(e: JournalEntry, filter: string, errorsOnly: boolean): boolean {
  if (errorsOnly && e.prio > 4) return false;
  const q = filter.trim().toLowerCase();
  return q === '' || e.msg.toLowerCase().includes(q) || (e.ident ?? '').toLowerCase().includes(q);
}

/** "14:03:07" today, "Oct 24 14:03:07" otherwise. */
export function entryTime(ms: number, now: Date = new Date()): string {
  const d = new Date(ms);
  const time = d.toLocaleTimeString('en-GB', { hour12: false });
  if (d.toDateString() === now.toDateString()) return time;
  return `${d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })} ${time}`;
}

/** Query string for GET /services/journal; cursor '' asks for the last entries. */
export function journalQuery(target: string, lines: number, cursor: string): string {
  const q = new URLSearchParams({ unit: target, lines: String(lines) });
  if (cursor) q.set('after', cursor);
  return q.toString();
}

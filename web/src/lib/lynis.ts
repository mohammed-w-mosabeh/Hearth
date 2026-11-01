// Lynis section of the Security page: types of internal/lynis, and where a
// finding is looked after in Hearth.

export interface LynisFinding {
  test: string;
  text: string;
  details?: string;
  solution?: string;
}

export interface LynisReport {
  time: number;
  index: number;
  version?: string;
  warnings: LynisFinding[];
  suggestions: LynisFinding[];
  truncated?: boolean;
}

export interface LynisRun {
  time: number;
  index: number;
  warnings: number;
  suggestions: number;
}

export type LynisSchedule = 'off' | 'weekly' | 'monthly';

export interface LynisView {
  installed: boolean;
  schedule: LynisSchedule;
  report?: LynisReport;
  running: boolean;
  error?: string;
  history: LynisRun[]; // newest first
}

// A test group's page: only groups a Hearth page really shows.
const PAGES: Record<string, string> = {
  BOOT: 'services',
  CONT: 'docker',
  DBS: 'docker',
  FILE: 'disks',
  FINT: 'security',
  FIRE: 'network',
  KRNL: 'hardware',
  LOGG: 'journal',
  NAME: 'network',
  NETW: 'network',
  PKGS: 'packages',
  PROC: 'resources',
  SCHD: 'scheduler',
  SSH: 'security',
  STRG: 'disks',
  TIME: 'services',
  USB: 'hardware',
};

/** The Hearth page for a test ID ("PKGS-7392" -> "#/packages"), or "". */
export function lynisPage(test: string): string {
  const group = test.split('-')[0] ?? '';
  const page = Object.hasOwn(PAGES, group) ? PAGES[group] : undefined;
  return page ? `#/${page}` : '';
}

/** The index a little under the last run's, for "how it went". */
export function lynisTrend(history: LynisRun[]): number | null {
  const [now, before] = history;
  return now && before ? now.index - before.index : null;
}

/** SVG polyline points of the index over time, oldest on the start side. */
export function lynisPoints(history: LynisRun[], width: number, height: number): string {
  if (history.length < 2) return '';
  const runs = [...history].reverse();
  const step = width / (runs.length - 1);
  return runs.map((r, i) => `${(i * step).toFixed(1)},${(height - (r.index / 100) * height).toFixed(1)}`).join(' ');
}

// Lists (ROADMAP 8.8): 10-minute sparklines in rows, the status shape of a
// row, and keyboard navigation between rows.

import type { StatsMsg, Tone } from './docker';
import type { StatusKind } from './status';
import type { ProcGroup } from './types';

/** How far back a row's sparkline reaches, in seconds. */
export const SPARK_S = 600;

export type Pt = [number, number]; // [unix seconds, value]

/**
 * Recent values per row id. It fills while a list follows its topic and
 * lasts for the session, so leaving a page and coming back keeps the line.
 * No per-item history is stored server side, except Docker's 5 minutes.
 */
export class RowHistory {
  #rows = new Map<string, Pt[]>();

  /** Adds v for id at t (seconds), dropping what is older than SPARK_S. */
  add(id: string, t: number, v: number): void {
    const pts = this.#rows.get(id) ?? [];
    const last = pts[pts.length - 1];
    if (last && last[0] >= t) return;
    pts.push([t, v]);
    let cut = 0;
    while (cut < pts.length && (pts[cut]?.[0] ?? t) < t - SPARK_S) cut++;
    this.#rows.set(id, cut > 0 ? pts.slice(cut) : pts);
  }

  /** Forgets rows with nothing newer than SPARK_S before now. */
  prune(now: number): void {
    for (const [id, pts] of this.#rows) if ((pts[pts.length - 1]?.[0] ?? 0) < now - SPARK_S) this.#rows.delete(id);
  }

  get(id: string): Pt[] {
    return this.#rows.get(id) ?? [];
  }

  /** A copy for $state.raw: the arrays of the rows as they are now. */
  snapshot(): Record<string, Pt[]> {
    const out: Record<string, Pt[]> = {};
    for (const [id, pts] of this.#rows) out[id] = pts.slice();
    return out;
  }
}

/** Adds a docker.stats message's CPU figures to h: the whole history
 *  hearthd keeps (5 minutes) when following starts, then each sample. */
export function addDockerStats(h: RowHistory, m: StatsMsg): void {
  if (m.kind === 'history') {
    for (const [id, pts] of Object.entries(m.history ?? {})) for (const p of pts) h.add(id, p.t, p.cpu);
  } else {
    for (const [id, p] of Object.entries(m.sample ?? {})) h.add(id, p.t, p.cpu);
  }
}

/** One per list, kept for the session. */
export const sparks = { units: new RowHistory(), containers: new RowHistory(), procs: new RowHistory() };

/**
 * An SVG path for pts in a w×h box: time across the last SPARK_S seconds up
 * to the newest point, values from 0 to their maximum (at least floor).
 * Points are averaged into at most w/2 buckets, so a row stays cheap.
 */
export function sparkPath(pts: Pt[], w: number, h: number, floor = 1): string {
  const end = pts[pts.length - 1]?.[0];
  if (end === undefined || pts.length < 2) return '';
  const start = end - SPARK_S;
  const n = Math.max(1, Math.floor(w / 2));
  const sum = new Array<number>(n).fill(0);
  const count = new Array<number>(n).fill(0);
  for (const [t, v] of pts) {
    if (t < start) continue;
    const i = Math.min(n - 1, Math.floor(((t - start) / SPARK_S) * n));
    sum[i] = (sum[i] ?? 0) + v;
    count[i] = (count[i] ?? 0) + 1;
  }
  const avg = sum.map((s, i) => ((count[i] ?? 0) > 0 ? s / (count[i] ?? 1) : null));
  const max = Math.max(floor, ...avg.filter((v): v is number => v !== null));
  // Empty buckets under a minute are bridged; a longer gap is when the
  // list was not open, and the line breaks there.
  const bridge = Math.ceil(60 / (SPARK_S / n));
  let d = '';
  let prev = -Infinity;
  avg.forEach((v, i) => {
    if (v === null) return;
    const x = ((i + 0.5) / n) * w;
    const y = h - 1 - (v / max) * (h - 2);
    d += `${i - prev <= bridge ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`;
    prev = i;
  });
  return d;
}

// Status shapes (docs/design/ember.md): ● ok, ◐ degraded, ▲ warning,
// ■ failed, □ stopped.

export function unitStatus(active: string): StatusKind {
  if (active === 'failed') return 'failed';
  if (active === 'active' || active === 'reloading') return 'ok';
  if (active === 'activating' || active === 'deactivating' || active === 'refreshing') return 'degraded';
  return 'stopped';
}

export function containerStatus(tone: Tone, health?: string): StatusKind {
  if (tone === 'failed') return 'failed';
  if (tone === 'running') return 'ok';
  if (tone === 'paused' || tone === 'changing') return health === 'unhealthy' ? 'warning' : 'degraded';
  return 'stopped';
}

export function groupStatus(g: Pick<ProcGroup, 'procs'>): StatusKind {
  return g.procs.length > 0 && g.procs.every((p) => p.state === 'T') ? 'degraded' : 'ok';
}

/**
 * Keyboard navigation for a table body: one row is in the Tab order; the
 * arrow keys, Home and End move between rows, and Enter opens the row's
 * [data-primary] element. Rows that come and go keep one tab stop.
 */
export function rowNav(tbody: HTMLTableSectionElement): { destroy: () => void } {
  const rows = (): HTMLTableRowElement[] => [...tbody.rows];
  const settle = (): void => {
    const all = rows();
    let current = all.find((r) => r.tabIndex === 0 && r.getAttribute('tabindex') === '0');
    for (const r of all) if (r !== current) r.setAttribute('tabindex', '-1');
    if (!current && all[0]) {
      current = all[0];
      current.setAttribute('tabindex', '0');
    }
  };
  const move = (to: HTMLTableRowElement | undefined): void => {
    if (!to) return;
    for (const r of rows()) r.setAttribute('tabindex', r === to ? '0' : '-1');
    to.focus();
  };
  const onkeydown = (e: KeyboardEvent): void => {
    const row = e.target;
    if (!(row instanceof HTMLTableRowElement) || row.parentElement !== tbody) return;
    const all = rows();
    const i = all.indexOf(row);
    let to: HTMLTableRowElement | undefined;
    if (e.key === 'ArrowDown') to = all[i + 1];
    else if (e.key === 'ArrowUp') to = all[i - 1];
    else if (e.key === 'Home') to = all[0];
    else if (e.key === 'End') to = all[all.length - 1];
    else if (e.key === 'Enter') {
      const primary = row.querySelector<HTMLElement>('[data-primary]');
      if (primary) {
        e.preventDefault();
        primary.click();
      }
      return;
    } else return;
    e.preventDefault();
    move(to);
  };
  // A click or Tab into a row's controls makes that row the tab stop.
  const onfocusin = (e: FocusEvent): void => {
    const row = e.target instanceof HTMLElement ? e.target.closest('tr') : null;
    if (row && row.parentElement === tbody && row.getAttribute('tabindex') !== '0') {
      for (const r of rows()) r.setAttribute('tabindex', r === row ? '0' : '-1');
    }
  };
  settle();
  const obs = new MutationObserver(settle);
  obs.observe(tbody, { childList: true });
  tbody.addEventListener('keydown', onkeydown);
  tbody.addEventListener('focusin', onfocusin);
  return {
    destroy: () => {
      obs.disconnect();
      tbody.removeEventListener('keydown', onkeydown);
      tbody.removeEventListener('focusin', onfocusin);
    },
  };
}

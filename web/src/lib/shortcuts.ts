// Keyboard navigation: "g" then a letter goes to a page, "?" shows them all.

/** The pages with a shortcut, by the letter after "g". */
export const GOTO: Readonly<Record<string, string>> = {
  h: 'home',
  r: 'resources',
  k: 'disks',
  a: 'apps',
  s: 'services',
  d: 'docker',
  p: 'packages',
  n: 'network',
  t: 'terminal',
  c: 'scheduler',
  j: 'journal',
  l: 'alerts',
  e: 'settings',
  q: 'games',
  o: 'tools',
  w: 'hardware',
  v: 'space',
  i: 'insight',
  x: 'security',
  b: 'tokens',
  m: 'modes',
  y: 'changes',
  f: 'reports',
};

/** How long the letter after "g" may take. */
export const SEQUENCE_MS = 1500;

export interface Keypress {
  key: string;
  code: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  target: EventTarget | null;
}

/** The latin letter pressed: on an Arabic layout key is "ل", code still "KeyG". */
export function letter(e: Pick<Keypress, 'key' | 'code'>): string {
  if (/^[a-z?]$/i.test(e.key)) return e.key.toLowerCase();
  const m = /^Key([A-Z])$/.exec(e.code);
  return m?.[1]?.toLowerCase() ?? (e.key === '؟' ? '?' : '');
}

/** Whether keys belong to something else: text being typed, a terminal, a dialog. */
export function ignored(e: Keypress): boolean {
  if (e.ctrlKey || e.metaKey || e.altKey) return true;
  const el = e.target as Partial<Element> | null;
  if (typeof el?.closest !== 'function') return false;
  return el.closest('input, textarea, select, button[role="combobox"], [contenteditable], dialog, .xterm') !== null;
}

export type Outcome = { go: string } | { help: true } | null;

/** Follows "g" sequences; press returns what a key means. */
export class Sequence {
  private since: number | null = null; // when "g" was pressed

  press(e: Keypress, now: number): Outcome {
    const since = this.since;
    this.since = null;
    if (ignored(e)) return null;
    const k = letter(e);
    if (since !== null && now - since <= SEQUENCE_MS) {
      const page = GOTO[k];
      return page ? { go: page } : null;
    }
    if (k === '?') return { help: true };
    if (k === 'g') this.since = now;
    return null;
  }
}

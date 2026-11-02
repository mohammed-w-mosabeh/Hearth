// Integrity watch on the Security page: types of GET /security/integrity.

export type IntegrityKind = 'added' | 'changed' | 'removed';

export interface IntegrityChange {
  path: string;
  root: string;
  kind: IntegrityKind;
  hash: string;
}

export interface IntegritySystemDir {
  dir: string;
  readable: boolean;
  names: string[];
  truncated?: boolean;
}

export interface IntegrityView {
  since: number;
  scanned: number;
  watched: number;
  truncated?: boolean;
  changes: IntegrityChange[];
  more?: number;
  system: IntegritySystemDir[];
  error?: string;
}

export interface IntegrityLine {
  op: '+' | '-' | ' ';
  text: string;
  gap?: boolean;
}

export interface IntegrityDiff {
  path: string;
  lines: IntegrityLine[];
  truncated?: boolean;
  note?: 'notext' | 'hidden';
}

/** What an accept names: each change as it was shown, so a newer one stays pending. */
export function acceptBody(changes: IntegrityChange[]): string {
  return JSON.stringify({ items: changes.map(({ path, hash }) => ({ path, hash })) });
}

/** The page's URL for a path's diff. */
export function diffUrl(path: string): string {
  return `/security/integrity/diff?path=${encodeURIComponent(path)}`;
}

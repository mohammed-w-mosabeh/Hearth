// Profiles page: types of internal/profiles and helpers to show them.

import type { App } from './apps';
import type { Snapshot } from './docker';
import type { MessageKey } from './i18n';
import type { Unit } from './services';

/** A stack is a managed app from a mode file (ROADMAP 10.4): shown and kept,
 *  picked in the mode editor (10.9), not here. */
export type Kind = 'unit' | 'container' | 'project' | 'app' | 'stack' | 'setting' | 'connection';
export type Do = 'start' | 'stop';

export const KINDS: Kind[] = ['unit', 'container', 'project', 'app'];

export interface Item {
  kind: Kind;
  target: string;
  do: Do;
}

export interface Active {
  since: string;
  by: 'user' | 'game';
  undo: Item[];
}

export interface Profile {
  id: number;
  name: string;
  items: Item[];
  auto_game: boolean;
  active?: Active;
}

export interface Draft {
  id?: number;
  name: string;
  items: Item[];
  auto_game: boolean;
}

export interface Candidate {
  value: string;
  label: string;
}

export type Template = 'dev' | 'gaming' | 'battery';
export const TEMPLATES: Template[] = ['dev', 'gaming', 'battery'];

export function templateKey(t: Template): MessageKey {
  return `profiles.template.${t}`;
}

export function draftFrom(t: Template, name: string): Draft {
  return { name, items: [], auto_game: t === 'gaming' };
}

export function kindKey(k: Kind): MessageKey {
  return `profiles.kind.${k}`;
}

/** Whether d is start or stop; a setting's value is shown as it is. */
export function isDo(d: string): d is Do {
  return d === 'start' || d === 'stop';
}

export function doKey(d: Do): MessageKey {
  return d === 'start' ? 'profiles.do.start' : 'profiles.do.stop';
}

/** Whether it can run with nobody there: others may ask for a password (S15a). */
export function unattended(it: Pick<Item, 'kind' | 'target'>): boolean {
  return it.kind === 'app' || it.kind === 'setting' || it.kind === 'connection' || (it.kind === 'unit' && it.target.startsWith('user/'));
}

/** Apps are started but never closed: that could lose unsaved work. */
export function canStop(kind: Kind): boolean {
  return kind !== 'app';
}

/** Same thing listed already, whatever is done to it. */
export function listed(items: readonly Item[], it: Pick<Item, 'kind' | 'target'>): boolean {
  return items.some((x) => x.kind === it.kind && x.target === it.target);
}

/** What can be picked for a kind; auto profiles only offer what asks for no password. */
export function candidates(kind: Kind, units: readonly Unit[], docker: Snapshot | null, apps: readonly App[], auto: boolean): Candidate[] {
  let out: Candidate[];
  switch (kind) {
    case 'unit':
      out = units.filter((u) => !u.critical).map((u) => ({ value: `${u.scope}/${u.name}`, label: u.description ?? u.name }));
      break;
    case 'container':
      out = (docker?.containers ?? []).map((c) => ({ value: c.name, label: c.image }));
      break;
    case 'project':
      out = (docker?.projects ?? []).map((p) => ({ value: p.name, label: p.name }));
      break;
    case 'app':
      out = apps.map((a) => ({ value: a.id, label: a.name }));
      break;
    case 'stack':
    case 'setting':
    case 'connection':
      out = []; // picked in the mode editor
      break;
  }
  return out.filter((c) => !auto || unattended({ kind, target: c.value })).sort((a, b) => a.value.localeCompare(b.value));
}

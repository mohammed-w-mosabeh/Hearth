// Modes page: types of internal/modes and of the profile engine that runs
// them (internal/profiles), and helpers to show them.

import type { App } from './apps';
import type { Snapshot } from './docker';
import type { MessageKey } from './i18n';
import type { Unit } from './services';

/** What a mode's switch does, as the profile engine lists it: its session
 *  settings are `setting` items and its managed apps `stack` items. */
export type Kind = 'unit' | 'container' | 'project' | 'app' | 'stack' | 'setting' | 'connection';
export type Do = 'start' | 'stop';

/** Kinds picked from a list in the editor; settings have their own fields. */
export const KINDS: Kind[] = ['unit', 'container', 'project', 'app', 'connection'];

export interface Item {
  kind: Kind;
  target: string;
  /** start or stop; a setting's value. */
  do: string;
}

/** A mode as the profile engine shows it (GET /profiles). */
export interface Profile {
  id: number;
  name: string;
  items: Item[];
  auto_game: boolean;
  active?: { since: string; by: string; undo: Item[] };
}

export interface Draft {
  id?: number;
  name: string;
  items: Item[];
  auto_game: boolean;
}

export type TriggerKind = 'schedule' | 'wifi' | 'battery' | 'display' | 'game';

export interface Trigger {
  kind: TriggerKind;
  on_calendar?: string;
  connection?: string;
  below?: number;
  event?: 'connect' | 'disconnect';
}

export interface Session {
  wallpaper?: string;
  do_not_disturb?: boolean;
  brightness?: number;
  power_profile?: string;
  open?: string[];
  connect?: string[];
  disconnect?: string[];
}

export interface Mode {
  id: string;
  hash: string;
  name: string;
  session: Session;
  stacks: { name: string; do: Do }[];
  items: Item[];
  triggers: Trigger[];
  /** Indexes of the triggers armed for this version of the file. */
  armed: number[];
  /** Its ID for the profile engine (profile/activate, /profiles/{id}). */
  number: number;
}

export interface ActiveMode {
  id: string;
  number: number;
  since: string;
  by: string;
  /** The file is no longer the one switched on, or is gone. */
  changed: boolean;
}

export interface Approval {
  id: number;
  mode: string;
  name: string;
  /** The trigger's kind. */
  reason: string;
  items: Item[];
  created_at: string;
}

export interface FileError {
  file: string;
  error: string;
}

export interface ModeList {
  modes: Mode[];
  errors: FileError[];
  active: ActiveMode[];
  approvals: Approval[];
}

/** A saved NetworkManager connection (GET /system/network). */
export interface Connection {
  uuid: string;
  name: string;
  type: string;
}

export const SETTINGS = ['power-profile', 'brightness', 'dnd', 'wallpaper'] as const;
export type Setting = (typeof SETTINGS)[number];

export function settingKey(s: Setting): MessageKey {
  return `modes.setting.${s}`;
}

export function isSetting(s: string): s is Setting {
  return (SETTINGS as readonly string[]).includes(s);
}

/** How many session settings and connections a mode sets (apps are counted with the rest). */
export function sessionCount(s: Session): number {
  let n = (s.connect?.length ?? 0) + (s.disconnect?.length ?? 0);
  for (const v of [s.wallpaper, s.do_not_disturb, s.brightness, s.power_profile]) if (v !== undefined && v !== '') n++;
  return n;
}

/** A trigger in words, without its key: the key comes from triggerKey. */
export function triggerDetail(t: Trigger, connections: readonly Connection[]): string {
  switch (t.kind) {
    case 'schedule':
      return t.on_calendar ?? '';
    case 'wifi':
      return connections.find((c) => c.uuid === t.connection)?.name ?? t.connection ?? '';
    default:
      return '';
  }
}

export function triggerKey(t: Trigger): MessageKey {
  if (t.kind === 'display') return t.event === 'disconnect' ? 'modes.display.disconnect' : 'modes.display.connect';
  return `modes.trigger.${t.kind}`;
}

export type Template = 'dev' | 'gaming' | 'battery';
export const TEMPLATES: Template[] = ['dev', 'gaming', 'battery'];

export function templateKey(t: Template): MessageKey {
  return `modes.template.${t}`;
}

export function draftFrom(t: Template, name: string): Draft {
  return { name, items: [], auto_game: t === 'gaming' };
}

export function kindKey(k: Kind): MessageKey {
  return `modes.kind.${k}`;
}

/** Whether d is start or stop; a setting's value is shown as it is. */
export function isDo(d: string): d is Do {
  return d === 'start' || d === 'stop';
}

export function doKey(d: Do, kind: Kind = 'unit'): MessageKey {
  if (kind === 'connection') return d === 'start' ? 'modes.do.connect' : 'modes.do.disconnect';
  return d === 'start' ? 'modes.do.start' : 'modes.do.stop';
}

/** Whether it can run with nobody there: others may ask for a password (S15a). */
export function unattended(it: Pick<Item, 'kind' | 'target'>): boolean {
  return it.kind === 'app' || it.kind === 'setting' || it.kind === 'connection' || (it.kind === 'unit' && it.target.startsWith('user/'));
}

/** Whether a mode with the game trigger may hold it: a managed app waits
 *  for approval or runs under a grant (ROADMAP 10.7, 10.8), never prompts. */
export function autoAllowed(it: Pick<Item, 'kind' | 'target'>): boolean {
  return it.kind === 'stack' || unattended(it);
}

/** Apps are started but never closed: that could lose unsaved work. */
export function canStop(kind: Kind): boolean {
  return kind !== 'app';
}

/** Same thing listed already, whatever is done to it. */
export function listed(items: readonly Item[], it: Pick<Item, 'kind' | 'target'>): boolean {
  return items.some((x) => x.kind === it.kind && x.target === it.target);
}

export interface Candidate {
  value: string;
  label: string;
}

/** What the editor can offer. */
export interface Sources {
  units: readonly Unit[];
  docker: Snapshot | null;
  apps: readonly App[];
  stacks: readonly string[];
  connections: readonly Connection[];
}

/** What can be picked for a kind; a game mode only offers what asks for no password. */
export function candidates(kind: Kind, src: Sources, auto: boolean): Candidate[] {
  let out: Candidate[];
  switch (kind) {
    case 'unit':
      out = src.units.filter((u) => !u.critical).map((u) => ({ value: `${u.scope}/${u.name}`, label: u.description ?? u.name }));
      break;
    case 'container':
      out = (src.docker?.containers ?? []).map((c) => ({ value: c.name, label: c.image }));
      break;
    case 'project':
      out = (src.docker?.projects ?? []).map((p) => ({ value: p.name, label: p.name }));
      break;
    case 'app':
      out = src.apps.map((a) => ({ value: a.id, label: a.name }));
      break;
    case 'stack':
      out = src.stacks.map((s) => ({ value: s, label: s }));
      break;
    case 'connection':
      out = src.connections.map((c) => ({ value: c.uuid, label: `${c.name} (${c.type})` }));
      break;
    case 'setting':
      out = [];
      break;
  }
  return out.filter((c) => !auto || autoAllowed({ kind, target: c.value })).sort((a, b) => a.value.localeCompare(b.value));
}

/** The setting items of a draft, by name, and the draft without them. */
export function splitSettings(items: readonly Item[]): { settings: Partial<Record<Setting, string>>; rest: Item[] } {
  const settings: Partial<Record<Setting, string>> = {};
  const rest: Item[] = [];
  for (const it of items) {
    if (it.kind === 'setting' && isSetting(it.target)) settings[it.target] = it.do;
    else rest.push(it);
  }
  return { settings, rest };
}

/** Setting items for the values given, in a fixed order; empty means leave it. */
export function settingItems(settings: Partial<Record<Setting, string>>): Item[] {
  const out: Item[] = [];
  for (const s of SETTINGS) {
    const v = settings[s]?.trim();
    if (v) out.push({ kind: 'setting', target: s, do: v });
  }
  return out;
}

// The rail of the Ember design (ROADMAP 8.4, docs/design/ember.md): its
// groups, the current item, and what the viewer folded.

import type { MessageKey } from './i18n';

export interface RailItem {
  /** A route id of router.svelte.ts: every existing URL keeps working. */
  id: string;
  label: MessageKey;
  icon: string;
}

export interface RailGroup {
  label: MessageKey;
  items: RailItem[];
}

export const railGroups: RailGroup[] = [
  { label: 'nav.groupOverview', items: [{ id: 'home', label: 'nav.home', icon: 'home' }] },
  {
    label: 'nav.groupWorkloads',
    items: [
      { id: 'apps', label: 'nav.desktopApps', icon: 'grid' },
      { id: 'stacks', label: 'nav.stacks', icon: 'layers' },
      { id: 'docker', label: 'nav.docker', icon: 'box' },
      { id: 'services', label: 'nav.services', icon: 'gear' },
      { id: 'games', label: 'nav.games', icon: 'gamepad' },
    ],
  },
  {
    label: 'nav.groupSystem',
    items: [
      { id: 'resources', label: 'nav.resources', icon: 'chart' },
      { id: 'disks', label: 'nav.disks', icon: 'disk' },
      { id: 'network', label: 'nav.network', icon: 'network' },
      { id: 'packages', label: 'nav.packages', icon: 'package' },
      { id: 'tools', label: 'nav.tools', icon: 'wrench' },
      { id: 'space', label: 'nav.space', icon: 'broom' },
      { id: 'removed', label: 'nav.removed', icon: 'trash' },
      { id: 'hardware', label: 'nav.hardware', icon: 'grid' },
      { id: 'terminal', label: 'nav.terminal', icon: 'terminal' },
      { id: 'settings', label: 'nav.settings', icon: 'sliders' },
    ],
  },
  {
    label: 'nav.groupAutomation',
    items: [
      { id: 'scheduler', label: 'nav.scheduler', icon: 'calendar' },
      { id: 'alerts', label: 'nav.alerts', icon: 'bell' },
      { id: 'modes', label: 'nav.modes', icon: 'layers' },
    ],
  },
  {
    label: 'nav.groupInsight',
    items: [
      { id: 'security', label: 'nav.security', icon: 'shield' },
      { id: 'changes', label: 'nav.changes', icon: 'history' },
      { id: 'reports', label: 'nav.reports', icon: 'report' },
      { id: 'journal', label: 'nav.journal', icon: 'book' },
      { id: 'insight', label: 'nav.insight', icon: 'bolt' },
      { id: 'tokens', label: 'nav.tokens', icon: 'shield' },
    ],
  },
];

/** Whether a rail item is the page on screen. */
export const railCurrent = (item: string, routeId: string): boolean => item === routeId;

const RAIL_KEY = 'hearth.rail';
const GROUPS_KEY = 'hearth.rail.groups';

/** The groups folded by hand. Unknown names do no harm: they match nothing. */
export function loadClosedGroups(storage: Pick<Storage, 'getItem'> | undefined = globalThis.localStorage): string[] {
  try {
    const v: unknown = JSON.parse(storage?.getItem(GROUPS_KEY) ?? '[]');
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return []; // blocked storage or a damaged value
  }
}

export function saveClosedGroups(closed: string[], storage: Pick<Storage, 'setItem'> | undefined = globalThis.localStorage): void {
  try {
    storage?.setItem(GROUPS_KEY, JSON.stringify(closed));
  } catch {
    // It still applies to this page.
  }
}

export function loadRailCollapsed(storage: Pick<Storage, 'getItem'> | undefined = globalThis.localStorage): boolean {
  try {
    return storage?.getItem(RAIL_KEY) === 'collapsed';
  } catch {
    return false; // storage can be blocked
  }
}

export function saveRailCollapsed(collapsed: boolean, storage: Pick<Storage, 'setItem'> | undefined = globalThis.localStorage): void {
  try {
    storage?.setItem(RAIL_KEY, collapsed ? 'collapsed' : 'open');
  } catch {
    // It still applies to this page.
  }
}

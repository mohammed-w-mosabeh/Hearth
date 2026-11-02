// The Ember redesign's prototype switch and rail (ROADMAP 8.1, docs/design/
// ember.md). `?ui=ember` turns it on for one page load; task 8.11 removes the
// switch and the old shell.

import type { MessageKey } from './i18n';

export const emberRequested = (search: string): boolean => new URLSearchParams(search).get('ui') === 'ember';

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
      { id: 'profiles', label: 'nav.profiles', icon: 'layers' },
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

const RAIL_KEY = 'hearth.rail';

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

import type { MessageKey } from './i18n';

// Hash routing (#/docker) keeps the Go side a plain static file server.
// A page may take a search through "?q=" (#/services?q=cups).

export interface Route {
  id: string;
  label: MessageKey;
  icon: string;
}

export const routes: Route[] = [
  { id: 'home', label: 'nav.home', icon: 'home' },
  { id: 'resources', label: 'nav.resources', icon: 'chart' },
  { id: 'disks', label: 'nav.disks', icon: 'disk' },
  { id: 'apps', label: 'nav.apps', icon: 'grid' },
  { id: 'games', label: 'nav.games', icon: 'gamepad' },
  { id: 'services', label: 'nav.services', icon: 'gear' },
  { id: 'docker', label: 'nav.docker', icon: 'box' },
  { id: 'packages', label: 'nav.packages', icon: 'package' },
  { id: 'tools', label: 'nav.tools', icon: 'wrench' },
  { id: 'space', label: 'nav.space', icon: 'broom' },
  { id: 'network', label: 'nav.network', icon: 'network' },
  { id: 'terminal', label: 'nav.terminal', icon: 'terminal' },
  { id: 'scheduler', label: 'nav.scheduler', icon: 'calendar' },
  { id: 'insight', label: 'nav.insight', icon: 'bolt' },
  { id: 'journal', label: 'nav.journal', icon: 'book' },
  { id: 'alerts', label: 'nav.alerts', icon: 'bell' },
  { id: 'security', label: 'nav.security', icon: 'shield' },
  { id: 'profiles', label: 'nav.profiles', icon: 'layers' },
  { id: 'changes', label: 'nav.changes', icon: 'history' },
  { id: 'reports', label: 'nav.reports', icon: 'report' },
  { id: 'settings', label: 'nav.settings', icon: 'sliders' },
];

export function routeId(hash: string): string {
  const id = hash.replace(/^#\/?/, '').replace(/\?.*$/, '');
  // "#k=<key>" is the session hand-off from /auth (session.ts strips it with
  // replaceState, which fires no hashchange): it means Home.
  return id === '' || id.startsWith('k=') ? 'home' : id;
}

/** The "q" search of a hash ("#/services?q=cups" -> "cups"), or "". */
export function routeQuery(hash: string): string {
  const m = /\?(?:.*&)?q=([^&]*)/.exec(hash);
  if (!m) return '';
  try {
    return decodeURIComponent((m[1] ?? '').replace(/\+/g, ' '));
  } catch {
    return ''; // malformed escape
  }
}

export const router = $state({ id: routeId(location.hash), q: routeQuery(location.hash) });

window.addEventListener('hashchange', () => {
  router.id = routeId(location.hash);
  router.q = routeQuery(location.hash);
});

export function href(id: string, q = ''): string {
  const base = id === 'home' ? '#/' : `#/${id}`;
  return q ? `${base}?q=${encodeURIComponent(q)}` : base;
}

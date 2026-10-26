import type { MessageKey } from './i18n';

// Hash routing (#/docker) keeps the Go side a plain static file server.
// A page may take a search through "?q=" (#/services?q=cups).

export interface Route {
  id: string;
  label: MessageKey;
  icon: string;
  phase: number;
}

export const routes: Route[] = [
  { id: 'home', label: 'nav.home', icon: 'home', phase: 1 },
  { id: 'resources', label: 'nav.resources', icon: 'chart', phase: 1 },
  { id: 'apps', label: 'nav.apps', icon: 'grid', phase: 1 },
  { id: 'services', label: 'nav.services', icon: 'gear', phase: 1 },
  { id: 'docker', label: 'nav.docker', icon: 'box', phase: 2 },
  { id: 'packages', label: 'nav.packages', icon: 'package', phase: 3 },
  { id: 'network', label: 'nav.network', icon: 'network', phase: 3 },
  { id: 'terminal', label: 'nav.terminal', icon: 'terminal', phase: 3 },
  { id: 'alerts', label: 'nav.alerts', icon: 'bell', phase: 1 },
  { id: 'settings', label: 'nav.settings', icon: 'sliders', phase: 4 },
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

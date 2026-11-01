// Event markers on charts (ROADMAP 6.2): GET /api/v1/markers, internal/changes.

import { api } from './api';
import { t } from './i18n';

export interface Marker {
  t: number; // unix ms
  kind: 'boot' | 'upgrade' | 'alert' | 'profile' | 'container' | string;
  subject?: string;
  count?: number;
}

export function markerText(m: Marker): string {
  const name = m.subject ?? '';
  switch (m.kind) {
    case 'boot':
      return t('marker.boot');
    case 'upgrade':
      return (m.count ?? 1) > 1 ? t('marker.upgrades', { n: m.count ?? 1 }) : t('marker.upgrade', { name });
    case 'alert':
      return t('marker.alert', { name });
    case 'profile':
      return t('marker.profile', { name });
    case 'container':
      return t('marker.container', { name });
    default:
      return name;
  }
}

/** The marker within reach of an x position, nearest first. */
export function nearest<T extends { x: number }>(items: readonly T[], x: number, reach: number): T | null {
  let best: T | null = null;
  for (const i of items) {
    if (Math.abs(i.x - x) <= reach && (best === null || Math.abs(i.x - x) < Math.abs(best.x - x))) best = i;
  }
  return best;
}

export async function loadMarkers(from: number, to: number): Promise<Marker[]> {
  const q = new URLSearchParams({ from: String(Math.floor(from)), to: String(Math.ceil(to)) });
  return (await api<{ markers: Marker[] }>(`/markers?${q.toString()}`)).markers;
}

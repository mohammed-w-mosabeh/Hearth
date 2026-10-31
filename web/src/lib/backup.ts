// Settings page: Hearth's own backups and the settings file (ROADMAP 5.8).

import type { Blocked, Item, Plan } from './actions';
import { t, type MessageKey } from './i18n';

/** GET /api/v1/backup. */
export interface Backups {
  dir: string;
  copies: { name: string; size: number; time: number }[];
}

const kinds: Record<string, MessageKey> = {
  setting: 'set.backup.kind.setting',
  weekly: 'set.backup.kind.setting',
  rule: 'set.backup.kind.rule',
  tile: 'set.backup.kind.tile',
  profile: 'set.backup.kind.profile',
};

const named = <T extends Item>(it: T): T => {
  const key = kinds[it.id.slice(0, it.id.indexOf(':'))];
  return { ...it, name: key ? `${t(key)}: ${it.name}` : it.name };
};

/** The import plan in the user's words: what each line is and what happens to it. */
export function wordPlan(plan: Plan): Plan {
  return {
    ...plan,
    items: plan.items.map((it) => ({ ...named(it), detail: t(it.detail === 'add' ? 'set.backup.add' : 'set.backup.update') })),
    blocked: plan.blocked.map((b: Blocked) => named(b)),
  };
}

/** The download name for the exported file. */
export function exportName(now: Date): string {
  return `hearth-settings-${now.toISOString().slice(0, 10)}.json`;
}

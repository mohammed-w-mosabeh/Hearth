import { describe, expect, it } from 'vitest';
import type { Plan } from './actions';
import { exportName, wordPlan } from './backup';
import en from './i18n/en.json';

const plan = (over: Partial<Plan>): Plan => ({ summary: '', items: [], blocked: [], risk: 'destructive', ...over });

describe('backup', () => {
  it('words the items of an import', () => {
    const p = wordPlan(
      plan({
        items: [
          { id: 'rule:Disk', name: 'Disk', detail: 'add' },
          { id: 'tile:Grafana', name: 'Grafana', detail: 'update' },
        ],
      }),
    );
    expect(p.items[0]).toMatchObject({ name: `${en['set.backup.kind.rule']}: Disk`, detail: en['set.backup.add'] });
    expect(p.items[1]).toMatchObject({ name: `${en['set.backup.kind.tile']}: Grafana`, detail: en['set.backup.update'] });
  });
  it('keeps why an entry is refused', () => {
    const p = wordPlan(plan({ blocked: [{ id: 'profile:a', name: 'a', detail: 'listed twice', reason: 'invalid' }] }));
    expect(p.blocked[0]).toMatchObject({ name: `${en['set.backup.kind.profile']}: a`, detail: 'listed twice' });
  });
  it('dates the file', () => {
    expect(exportName(new Date('2026-10-31T12:00:00Z'))).toBe('hearth-settings-2026-10-31.json');
  });
});

import { describe, expect, it } from 'vitest';
import { KINDS, SOURCES, byDay, diffEmpty, diffRows, eventDetail, eventKey, sourceKey, timelineURL, toggled, type SectionDiff, type TimelineEvent } from './changes';
import en from './i18n/en.json';

const ev = (over: Partial<TimelineEvent>): TimelineEvent => ({ t: '2026-10-29T10:00:00Z', source: 'packages', kind: 'installed', subject: 'vim:amd64', ...over });
const section = (over: Partial<SectionDiff>): SectionDiff => ({ source: 'units', added: [], removed: [], changed: [], ...over });

describe('changes', () => {
  it('has a message for every key it builds', () => {
    const keys: string[] = ['changes.source.other', sourceKey('future')];
    for (const s of SOURCES) keys.push(sourceKey(s));
    for (const [source, kinds] of [
      ['packages', ['installed', 'upgraded', 'removed', 'purged']],
      ['units', ['added', 'deleted', 'changed']],
      ['containers', ['added', 'deleted', 'changed']],
      ['listeners', ['added', 'deleted', 'changed']],
    ] as const) {
      for (const kind of kinds) keys.push(eventKey(ev({ source, kind })) ?? '');
    }
    for (const k of keys) expect(en).toHaveProperty([k]);
    expect(KINDS).toHaveLength(7);
  });
  it('leaves Hearth actions and unknown events to their own names', () => {
    expect(eventKey(ev({ source: 'hearth', kind: 'action', action: 'unit.restart' }))).toBeNull();
    expect(eventKey(ev({ source: 'future' }))).toBeNull();
    expect(eventKey(ev({ kind: 'future' }))).toBeNull();
  });
  it('words the detail of each kind of event', () => {
    expect(eventDetail(ev({ before: '1.0', after: '1.1' }))).toBe('1.0 → 1.1');
    expect(eventDetail(ev({ after: '1.1' }))).toBe('1.1');
    expect(eventDetail(ev({ before: '1.0' }))).toBe('1.0');
    expect(eventDetail(ev({}))).toBe('');
    expect(eventDetail(ev({ source: 'hearth', result: 'ok', origin: 'local' }))).toBe('ok (local)');
  });
  it('builds timeline URLs', () => {
    expect(timelineURL([])).toBe('/changes');
    expect(timelineURL([...SOURCES])).toBe('/changes');
    expect(timelineURL(['units'], 5)).toBe('/changes?before=5&source=units');
    expect(timelineURL(['units', 'hearth'])).toBe('/changes?source=units%2Chearth');
  });
  it('toggles a filter', () => {
    expect(toggled(['a'], 'b')).toEqual(['a', 'b']);
    expect(toggled(['a', 'b'], 'a')).toEqual(['b']);
  });
  it('groups events by day in order', () => {
    const list = [ev({ t: 'd2 b' }), ev({ t: 'd2 a' }), ev({ t: 'd1 a' })];
    const days = byDay(list, (iso) => iso.slice(0, 2));
    expect(days.map((d) => [d.day, d.events.length])).toEqual([['d2', 2], ['d1', 1]]);
  });
  it('flattens a diff and tells when nothing differs', () => {
    const s = section({ added: [{ key: 'a', after: '1' }], removed: [{ key: 'b', before: '2' }], changed: [{ key: 'c', before: '1', after: '2' }] });
    expect(diffRows(s).map((r) => [r.mark, r.key])).toEqual([['added', 'a'], ['removed', 'b'], ['changed', 'c']]);
    expect(diffEmpty([section({})])).toBe(true);
    expect(diffEmpty([s])).toBe(false);
    expect(diffEmpty([section({ missing: true })])).toBe(false);
  });
});

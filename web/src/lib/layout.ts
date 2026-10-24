// Home card layout: which cards show, in what order. Saved on the server
// as {order, hidden}; the cards that exist come from the machine (one per
// disk, per GPU...), so the saved layout is merged with them on every render.

export interface Layout {
  order: string[];
  hidden: string[];
}

const ID_RE = /^[a-z][a-z0-9]*(:[A-Za-z0-9._@:/ -]{1,190})?$/;

/** Keeps only well-formed, unique ids from untrusted saved data. */
export function sanitize(raw: unknown): Layout {
  const list = (v: unknown): string[] =>
    Array.isArray(v)
      ? [...new Set(v.filter((x): x is string => typeof x === 'string' && x.length <= 200 && ID_RE.test(x)))]
      : [];
  if (typeof raw !== 'object' || raw === null) return { order: [], hidden: [] };
  const r = raw as Record<string, unknown>;
  return { order: list(r.order), hidden: list(r.hidden) };
}

/**
 * Orders available cards by the saved order; cards the layout has never
 * seen go after the known ones, in their natural order. Saved ids for cards
 * that do not exist now (an unplugged disk) are kept in the layout so the
 * card returns to its place, but are not shown.
 */
export function arrange(layout: Layout, available: string[]): { visible: string[]; hidden: string[] } {
  const avail = new Set(available);
  const hidden = new Set(layout.hidden);
  const known = layout.order.filter((id) => avail.has(id));
  const fresh = available.filter((id) => !layout.order.includes(id));
  const all = [...known, ...fresh];
  return { visible: all.filter((id) => !hidden.has(id)), hidden: all.filter((id) => hidden.has(id)) };
}

/** Moves id to just before `before` (or to the end if null) among visible cards. */
export function moveTo(layout: Layout, visible: string[], id: string, before: string | null): Layout {
  if (id === before) return layout;
  const seq = visible.filter((x) => x !== id);
  const at = before === null ? seq.length : seq.indexOf(before);
  seq.splice(at < 0 ? seq.length : at, 0, id);
  return { ...layout, order: withAbsent(layout.order, seq) };
}

/** Moves id one step left (-1) or right (+1) among visible cards. */
export function step(layout: Layout, visible: string[], id: string, delta: -1 | 1): Layout {
  const i = visible.indexOf(id);
  const j = i + delta;
  if (i < 0 || j < 0 || j >= visible.length) return layout;
  const seq = visible.filter((x) => x !== id);
  seq.splice(j, 0, id);
  return { ...layout, order: withAbsent(layout.order, seq) };
}

export function setHidden(layout: Layout, id: string, hide: boolean): Layout {
  const hidden = layout.hidden.filter((x) => x !== id);
  if (hide) hidden.push(id);
  const order = layout.order.includes(id) ? layout.order : [...layout.order, id];
  return { order, hidden };
}

// withAbsent returns seq followed by saved ids not in it (absent or hidden
// cards), so reordering never forgets where they were.
function withAbsent(saved: string[], seq: string[]): string[] {
  const s = new Set(seq);
  return [...seq, ...saved.filter((x) => !s.has(x))];
}

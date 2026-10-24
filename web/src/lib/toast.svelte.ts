// Toasts: short status messages, optionally with an Undo (SPEC: reversible
// actions show Undo for 5 s).

export interface Toast {
  id: number;
  text: string;
  tone: 'info' | 'error';
  undo?: () => void;
}

export const UNDO_MS = 5000;
const INFO_MS = 4000;
const ERROR_MS = 8000;

let next = 1;
export const toasts = $state<Toast[]>([]);

export function dismiss(id: number): void {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

export function toast(text: string, opts: { tone?: Toast['tone']; undo?: () => void } = {}): void {
  const t: Toast = { id: next++, text, tone: opts.tone ?? 'info', ...(opts.undo ? { undo: opts.undo } : {}) };
  toasts.push(t);
  setTimeout(() => dismiss(t.id), t.undo ? UNDO_MS : t.tone === 'error' ? ERROR_MS : INFO_MS);
}

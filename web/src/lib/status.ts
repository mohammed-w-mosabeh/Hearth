// Status is never colour alone (ROADMAP 8.2): each kind has its shape.

export type StatusKind = 'ok' | 'degraded' | 'warning' | 'failed' | 'stopped';

export const statusShape: Record<StatusKind, string> = { ok: '●', degraded: '◐', warning: '▲', failed: '■', stopped: '□' };

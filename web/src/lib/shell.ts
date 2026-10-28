// Terminal page: shell sessions (internal/terminal) and how other pages
// ask for one ("open terminal here").

export type Phase = 'auth' | 'ready' | 'exit';

/** The WebSocket URL of a new shell, in dir (home when ""). */
export function shellURL(base: string, cols: number, rows: number, dir = ''): string {
  const url = new URL('/api/v1/terminal', base);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  const q = new URLSearchParams({ cols: String(cols), rows: String(rows) });
  if (dir) q.set('dir', dir);
  url.search = q.toString();
  return url.toString();
}

/** What a terminal tab runs. */
export type Open =
  | { kind: 'shell'; dir: string; prefill: string }
  | { kind: 'container'; id: string; name: string };

// Other pages link to #/terminal?q=<request>: "dir:/path", "tool:<name>",
// "container:<id>:<name>".
export const openDir = (dir: string) => `dir:${dir}`;
export const openTool = (name: string) => `tool:${name}`;
export const openContainer = (id: string, name: string) => `container:${id}:${name}`;

const toolRe = /^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,127}$/;
const idRe = /^[a-f0-9]{12,64}$/;

/** Reads an "open terminal here" request; null when it is not one. */
export function parseOpen(q: string): Open | null {
  const [kind, ...rest] = q.split(':');
  const arg = rest.join(':');
  switch (kind) {
    case 'dir':
      return arg.startsWith('/') ? { kind: 'shell', dir: arg, prefill: '' } : null;
    case 'tool':
      // Typed at the prompt, never run: the user presses Enter.
      return toolRe.test(arg) ? { kind: 'shell', dir: '', prefill: `${arg} ` } : null;
    case 'container': {
      const [id = '', ...name] = rest;
      return idRe.test(id) ? { kind: 'container', id, name: name.join(':') || id.slice(0, 12) } : null;
    }
    default:
      return null;
  }
}

/** A tab's title: the folder's last part, the container's name, or the shell. */
export function tabTitle(o: Open, n: number): string {
  if (o.kind === 'container') return o.name;
  if (o.prefill) return o.prefill.trim();
  if (o.dir) return o.dir === '/' ? '/' : (o.dir.split('/').pop() ?? o.dir);
  return n > 1 ? `~ (${n})` : '~';
}

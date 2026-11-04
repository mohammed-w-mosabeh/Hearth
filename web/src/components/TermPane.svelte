<script lang="ts">
  import { lightAnsi } from '../lib/termtheme';
  import type { FitAddon } from '@xterm/addon-fit';
  import type { Terminal } from '@xterm/xterm';
  import { sessionKey } from '../lib/api';
  import type { Phase } from '../lib/shell';

  // One terminal session: xterm.js (loaded on demand) bridged to a
  // WebSocket that carries terminal bytes as binary frames and control as
  // text frames ({"state":"auth"|"ready"|"exit","error":...} in,
  // {"resize":[cols,rows]} out). Used by the Terminal page and the
  // container shell. Bumping session opens a new one.
  let {
    url,
    session,
    prefill = '',
    visible = true,
    onphase,
  }: {
    url: (cols: number, rows: number) => string;
    session: number;
    prefill?: string;
    visible?: boolean;
    onphase: (phase: Phase, error: string) => void;
  } = $props();

  let el = $state<HTMLDivElement | null>(null);
  let refit = (): void => {};

  $effect(() => {
    if (session === 0 || !el) return;
    const host = el;
    let disposed = false;
    let ws: WebSocket | null = null;
    let term: Terminal | null = null;
    let undoStyles = (): void => {};
    let ro: ResizeObserver | null = null;
    let phase: Phase = 'auth';
    let error = '';
    const set = (p: Phase): void => {
      phase = p;
      onphase(p, error);
    };

    async function start(): Promise<void> {
      const mod = await import('../lib/terminal');
      if (disposed) return;
      undoStyles = mod.cspStyles(document);
      const css = getComputedStyle(host);
      const color = (v: string) => css.getPropertyValue(v).trim();
      const tm = new mod.Terminal({
        convertEol: false,
        // Blinking loops, so it stops under reduced motion (ROADMAP 8.9).
        cursorBlink: !matchMedia('(prefers-reduced-motion: reduce)').matches,
        fontFamily: color('--font-mono'),
        fontSize: 13,
        scrollback: 5000,
        theme: {
          background: color('--bg'),
          foreground: color('--text'),
          cursor: color('--accent'),
          selectionBackground: color('--surface-2'),
          ...(document.documentElement.dataset.theme === 'light' ? lightAnsi : {}),
        },
      });
      term = tm;
      const fit: FitAddon = new mod.FitAddon();
      tm.loadAddon(fit);
      tm.open(host);
      fit.fit();
      refit = () => fit.fit();

      const key = sessionKey();
      const sock = new WebSocket(url(tm.cols, tm.rows), ['hearth.v1', `hearth.key.${key ?? ''}`]);
      sock.binaryType = 'arraybuffer';
      ws = sock;
      const enc = new TextEncoder();
      // The prefill is typed once the shell has printed its prompt; it is
      // never followed by Enter.
      let pending = prefill;
      sock.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          tm.write(new Uint8Array(ev.data));
          if (pending && phase === 'ready') {
            sock.send(enc.encode(pending));
            pending = '';
          }
          return;
        }
        let m: { state?: string; error?: string };
        try {
          m = JSON.parse(String(ev.data)) as typeof m;
        } catch {
          return;
        }
        if (m.error) error = m.error;
        if (m.state === 'auth' || m.state === 'ready' || m.state === 'exit') set(m.state);
        if (m.state === 'ready') tm.focus();
      };
      sock.onclose = () => {
        if (!disposed && phase !== 'exit') set('exit');
      };
      tm.onData((d) => {
        if (sock.readyState === WebSocket.OPEN && phase === 'ready') sock.send(enc.encode(d));
      });
      tm.onResize(({ cols, rows }) => {
        if (sock.readyState === WebSocket.OPEN) sock.send(JSON.stringify({ resize: [cols, rows] }));
      });
      ro = new ResizeObserver(() => {
        if (host.offsetParent !== null) fit.fit(); // not while hidden
      });
      ro.observe(host);
    }

    set('auth');
    void start();
    return () => {
      disposed = true;
      refit = () => {};
      ro?.disconnect();
      ws?.close();
      term?.dispose();
      undoStyles();
    };
  });

  // A tab shown again fits its (possibly resized) box.
  $effect(() => {
    if (visible) setTimeout(() => refit(), 0);
  });
</script>

{#if session > 0}
  <div class="term" bind:this={el}></div>
{/if}

<style>
  .term {
    flex: 1;
    min-height: 300px;
    padding: var(--space-2);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow: hidden;
  }
</style>

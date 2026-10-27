<script lang="ts">
  import type { FitAddon } from '@xterm/addon-fit';
  import type { Terminal } from '@xterm/xterm';
  import { sessionKey } from '../lib/api';
  import { execURL } from '../lib/docker';
  import { t } from '../lib/i18n';

  // A shell inside a container (SECURITY S12a): /bin/sh through
  // hearth-broker, which asks for the administrator password every time.
  // Opened only on request, never just by showing the tab.
  let { id, refused }: { id: string; refused: string[] } = $props();

  type Phase = 'idle' | 'auth' | 'ready' | 'exit';
  let phase = $state<Phase>('idle');
  let error = $state('');
  let el = $state<HTMLDivElement | null>(null);
  let session = $state(0); // bumped to open a new shell

  $effect(() => {
    if (session === 0 || !el) return;
    const host = el;
    let disposed = false;
    let ws: WebSocket | null = null;
    let term: Terminal | null = null;
    let undoStyles = (): void => {};
    let ro: ResizeObserver | null = null;

    async function start(): Promise<void> {
      const mod = await import('../lib/terminal');
      if (disposed) return;
      undoStyles = mod.cspStyles(document);
      const css = getComputedStyle(host);
      const color = (v: string) => css.getPropertyValue(v).trim();
      const tm = new mod.Terminal({
        convertEol: false,
        cursorBlink: true,
        fontFamily: color('--font-mono'),
        fontSize: 13,
        scrollback: 5000,
        theme: { background: color('--bg'), foreground: color('--text'), cursor: color('--accent'), selectionBackground: color('--surface-2') },
      });
      term = tm;
      const fit: FitAddon = new mod.FitAddon();
      tm.loadAddon(fit);
      tm.open(host);
      fit.fit();

      const key = sessionKey();
      const sock = new WebSocket(execURL(location.href, id, tm.cols, tm.rows), ['hearth.v1', `hearth.key.${key ?? ''}`]);
      sock.binaryType = 'arraybuffer';
      ws = sock;
      const enc = new TextEncoder();
      sock.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          tm.write(new Uint8Array(ev.data));
          return;
        }
        let m: { state?: string; error?: string };
        try {
          m = JSON.parse(String(ev.data)) as typeof m;
        } catch {
          return;
        }
        if (m.state === 'auth' || m.state === 'ready' || m.state === 'exit') phase = m.state;
        if (m.state === 'ready') tm.focus();
        if (m.error) error = m.error;
      };
      sock.onclose = () => {
        if (!disposed) phase = 'exit';
      };
      tm.onData((d) => {
        if (sock.readyState === WebSocket.OPEN && phase === 'ready') sock.send(enc.encode(d));
      });
      tm.onResize(({ cols, rows }) => {
        if (sock.readyState === WebSocket.OPEN) sock.send(JSON.stringify({ resize: [cols, rows] }));
      });
      ro = new ResizeObserver(() => fit.fit());
      ro.observe(host);
    }

    phase = 'auth';
    error = '';
    void start();
    return () => {
      disposed = true;
      ro?.disconnect();
      ws?.close();
      term?.dispose();
      undoStyles();
    };
  });

  function open(): void {
    session++;
  }
</script>

{#if refused.length > 0}
  <p class="note warn" role="note">{t('shell.refused', { why: refused.join(', ') })}</p>
{:else}
  <div class="bar">
    {#if phase === 'idle' || phase === 'exit'}
      <button type="button" class="plain" onclick={open}>{phase === 'exit' ? t('shell.reopen') : t('shell.open')}</button>
    {/if}
    <span class="note" role="status">
      {#if phase === 'idle'}{t('shell.intro')}{:else if phase === 'auth'}{t('shell.auth')}{:else if phase === 'exit'}{error
          ? t('shell.failed', { error })
          : t('shell.exited')}{/if}
    </span>
  </div>
  {#if session > 0}
    <div class="term" bind:this={el}></div>
  {/if}
{/if}

<style>
  .bar {
    display: flex;
    gap: var(--space-3);
    align-items: center;
    margin-bottom: var(--space-2);
    font-size: 13px;
  }
  .note {
    color: var(--text-muted);
    font-size: 13px;
  }
  .warn {
    border-inline-start: 3px solid var(--warn);
    padding-inline-start: var(--space-2);
  }
  .plain {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
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

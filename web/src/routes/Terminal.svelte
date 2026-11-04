<script lang="ts">
  import { tick } from 'svelte';
  import TermPane from '../components/TermPane.svelte';
  import { execURL } from '../lib/docker';
  import { isRTL, t } from '../lib/i18n';
  import { router } from '../lib/router.svelte';
  import { parseOpen, shellURL, tabTitle, type Open, type Phase } from '../lib/shell';

  // Terminal (SPEC 13, SECURITY S14): tabs of shells running as you, each
  // in its own systemd user unit, and container shells (S12a, through
  // hearth-broker). The page stays mounted while you visit other pages,
  // so its shells keep running; closing a tab ends its shell.
  let { active }: { active: boolean } = $props();

  interface Tab {
    key: number;
    open: Open;
    title: string;
    session: number;
    phase: Phase;
    error: string;
  }

  let tabs = $state<Tab[]>([]);
  let current = $state(0);
  let next = 1;
  let shells = 0;
  let started = false; // not reactive: only the first visit opens a shell

  function add(open: Open): void {
    if (open.kind === 'shell') shells++;
    const tab: Tab = { key: next++, open, title: tabTitle(open, shells), session: 1, phase: 'auth', error: '' };
    tabs.push(tab);
    current = tab.key;
  }

  function close(key: number): void {
    const i = tabs.findIndex((x) => x.key === key);
    if (i < 0) return;
    tabs.splice(i, 1);
    if (current === key) current = tabs[Math.min(i, tabs.length - 1)]?.key ?? 0;
  }

  function reopen(tab: Tab): void {
    tab.error = '';
    tab.session++;
  }

  // The first visit opens a shell; "open terminal here" from another page
  // (#/terminal?q=dir:/path, tool:<name>, container:<id>:<name>) opens a
  // tab for it, then the request is dropped from the address.
  $effect(() => {
    if (!active) return;
    const req = router.q ? parseOpen(router.q) : null;
    if (req) {
      add(req);
      history.replaceState(null, '', '#/terminal');
      router.q = '';
    } else if (!started) {
      add({ kind: 'shell', dir: '', prefill: '' });
    }
    started = true;
  });

  function url(tab: Tab): (cols: number, rows: number) => string {
    const o = tab.open;
    return o.kind === 'container' ? (c, r) => execURL(location.href, o.id, c, r) : (c, r) => shellURL(location.href, c, r, o.dir);
  }

  // The tab list's own keys (WAI-ARIA tabs): arrows and Home/End move,
  // Delete closes; only the current tab is in the Tab order.
  function tabKey(e: KeyboardEvent, key: number): void {
    const i = tabs.findIndex((x) => x.key === key);
    const focus = (k: number): void => void tick().then(() => document.getElementById(`term-tab-${k}`)?.focus());
    if (e.key === 'Delete') {
      e.preventDefault();
      close(key);
      const next = tabs[Math.min(i, tabs.length - 1)];
      if (next) focus(next.key);
      return;
    }
    const step = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
    const to =
      step !== undefined ? (i + (isRTL() ? -step : step) + tabs.length) % tabs.length : e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1 : -1;
    const target = tabs[to];
    if (!target) return;
    e.preventDefault();
    current = target.key;
    focus(target.key);
  }

  function onkeydown(e: KeyboardEvent): void {
    // Ctrl+Shift+T: a new shell (Ctrl+T belongs to the browser).
    if (active && e.ctrlKey && e.shiftKey && e.key.toLowerCase() === 't') {
      e.preventDefault();
      add({ kind: 'shell', dir: '', prefill: '' });
    }
  }
</script>

<svelte:window {onkeydown} />

<div class="page">
  <div class="tabs">
    <div class="list" role="tablist" aria-label={t('term.tabs')}>
    {#each tabs as tab (tab.key)}
      <div class="tab" class:on={tab.key === current}>
        <button
          type="button"
          role="tab"
          id="term-tab-{tab.key}"
          aria-selected={tab.key === current}
          aria-controls="term-panel-{tab.key}"
          aria-describedby="term-tab-hint"
          tabindex={tab.key === current ? 0 : -1}
          onkeydown={(e) => tabKey(e, tab.key)}
          title={tab.open.kind === 'shell' ? tab.open.dir || '~' : t('term.container', { name: tab.title })}
          onclick={() => (current = tab.key)}
        >
          {#if tab.open.kind === 'container'}<span class="kind">{t('term.kind.container')}</span>{/if}
          {tab.title}
          {#if tab.phase === 'exit'}<span class="ended">· {t('term.ended')}</span>{/if}
        </button>
        <!-- Out of the tab list's tree, as a tab list holds only tabs; Delete
             on the tab does the same, as does Close tab once the shell ends. -->
        <button type="button" class="x" aria-hidden="true" tabindex="-1" title={t('term.close', { name: tab.title })} onclick={() => close(tab.key)}>×</button>
      </div>
    {/each}
    </div>
    <span id="term-tab-hint" class="visually-hidden">{t('term.tabHint')}</span>
    <button type="button" class="new" title={t('term.newHint')} onclick={() => add({ kind: 'shell', dir: '', prefill: '' })}>+ {t('term.new')}</button>
  </div>

  {#if tabs.length === 0}
    <p class="note">{t('term.none')}</p>
  {/if}
  {#each tabs as tab (tab.key)}
    <div class="panel" id="term-panel-{tab.key}" role="tabpanel" aria-labelledby="term-tab-{tab.key}" hidden={tab.key !== current}>
      {#if tab.phase === 'auth' && tab.open.kind === 'container'}
        <p class="note" role="status">{t('shell.auth')}</p>
      {:else if tab.phase === 'exit'}
        <div class="bar" role="status">
          <span class="note">{tab.error ? t('term.failed', { error: tab.error }) : t('term.exited')}</span>
          <button type="button" class="plain" onclick={() => reopen(tab)}>{t('term.reopen')}</button>
          <button type="button" class="plain" onclick={() => close(tab.key)}>{t('term.closeTab')}</button>
        </div>
      {/if}
      <TermPane
        url={url(tab)}
        session={tab.session}
        prefill={tab.open.kind === 'shell' ? tab.open.prefill : ''}
        visible={active && tab.key === current}
        onphase={(p, e) => {
          tab.phase = p;
          tab.error = e;
        }}
      />
    </div>
  {/each}
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    align-items: center;
    margin-bottom: var(--space-2);
  }
  .list {
    display: contents;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    background: var(--surface);
    font-size: 13px;
  }
  .tab.on {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  .tab button {
    font: inherit;
    color: var(--text);
    background: none;
    border: 0;
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tab .x {
    color: var(--text-muted);
    padding-inline-start: 0;
  }
  .kind,
  .ended {
    color: var(--text-muted);
    font-size: 11px;
  }
  .kind {
    margin-inline-end: var(--space-1);
  }
  .new {
    font: inherit;
    font-size: 13px;
    color: var(--accent);
    background: none;
    border: 1px dashed var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
    cursor: pointer;
  }
  .panel {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .panel[hidden] {
    display: none;
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    align-items: center;
    margin-bottom: var(--space-2);
  }
  .note {
    color: var(--text-muted);
    font-size: 13px;
  }
  .plain {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
</style>

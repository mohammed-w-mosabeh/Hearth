<script lang="ts">
  import { run } from '../lib/actions';
  import { api, ApiError } from '../lib/api';
  import type { App } from '../lib/apps';
  import { t, type MessageKey } from '../lib/i18n';
  import { appEntries, pageEntries, search, unitEntries, type Entry } from '../lib/palette';
  import { href, routes } from '../lib/router.svelte';
  import type { ServicesMsg, Unit } from '../lib/services';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Ctrl+K: jump to a page, launch an app, or find a unit. Units come
  // from the "services" topic, followed only while the palette is open.
  let { open, live, onclose }: { open: boolean; live: Live | null; onclose: () => void } = $props();

  let dialog: HTMLDialogElement;
  let input = $state<HTMLInputElement | null>(null);
  let query = $state('');
  let active = $state(0);
  let apps = $state.raw<App[]>([]);
  let units = $state.raw<Unit[]>([]);

  // Ember's rail also names Apps and Modes, which open existing pages for now.
  const extra = document.documentElement.dataset['ui'] === 'ember' ? [{ id: 'stacks', label: 'nav.stacks' }, { id: 'modes', label: 'nav.modes' }] as const : [];
  const pages = $derived(pageEntries([...routes, ...extra].map((r) => ({ id: r.id, title: t(r.label) }))));
  const entries = $derived([...pages, ...appEntries(apps), ...unitEntries(units)]);
  const results = $derived(search(entries, query));

  $effect(() => {
    if (!open) {
      if (dialog.open) dialog.close();
      return;
    }
    query = '';
    active = 0;
    if (!dialog.open) dialog.showModal();
    input?.focus();
    api<{ apps: App[] }>('/apps')
      .then((r) => (apps = r.apps))
      .catch(() => {});
    return live?.subscribe('services', (data) => {
      const m = data as ServicesMsg;
      if (m?.kind === 'list' && Array.isArray(m.units)) units = m.units;
    });
  });

  // Keep the highlighted row valid as results change.
  $effect(() => {
    if (active >= results.length) active = Math.max(0, results.length - 1);
  });

  const kindLabel: Record<Entry['kind'], MessageKey> = { page: 'palette.kind.page', app: 'palette.kind.app', unit: 'palette.kind.unit' };

  async function choose(e: Entry | undefined): Promise<void> {
    if (!e) return;
    onclose();
    switch (e.kind) {
      case 'page':
        location.hash = href(e.id);
        break;
      case 'unit':
        location.hash = href('services', e.title);
        break;
      case 'app':
        try {
          await run('app', 'launch', e.id);
          toast(t('apps2.opening', { name: e.title }));
        } catch (err) {
          toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
        }
        break;
    }
  }

  function onkeydown(ev: KeyboardEvent): void {
    const n = results.length;
    switch (ev.key) {
      case 'ArrowDown':
        active = n ? (active + 1) % n : 0;
        break;
      case 'ArrowUp':
        active = n ? (active - 1 + n) % n : 0;
        break;
      case 'Home':
        if (!ev.ctrlKey) return;
        active = 0;
        break;
      case 'End':
        if (!ev.ctrlKey) return;
        active = Math.max(0, n - 1);
        break;
      case 'Enter':
        void choose(results[active]);
        break;
      default:
        return;
    }
    ev.preventDefault();
    document.getElementById(`palette-opt-${active}`)?.scrollIntoView({ block: 'nearest' });
  }
</script>

<dialog bind:this={dialog} aria-label={t('palette.label')} onclose={() => open && onclose()}>
  <input
    bind:this={input}
    bind:value={query}
    oninput={() => (active = 0)}
    {onkeydown}
    type="text"
    role="combobox"
    aria-expanded="true"
    aria-controls="palette-list"
    aria-autocomplete="list"
    aria-activedescendant={results.length ? `palette-opt-${active}` : undefined}
    placeholder={t('palette.placeholder')}
    autocomplete="off"
    spellcheck="false"
  />
  <ul id="palette-list" role="listbox" aria-label={t('palette.results')}>
    {#each results as e, i (`${e.kind}:${e.id}`)}
      <!-- Mouse users click; keyboard users stay in the input (combobox pattern). -->
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <li
        id={`palette-opt-${i}`}
        role="option"
        aria-selected={i === active}
        class:active={i === active}
        onclick={() => choose(e)}
        onmousemove={() => (active = i)}
      >
        <span class="title">{e.title}</span>
        {#if e.detail}<span class="detail">{e.detail}</span>{/if}
        <span class="kind">{t(kindLabel[e.kind])}</span>
      </li>
    {:else}
      <li class="empty" role="option" aria-selected="false" aria-disabled="true">{t('palette.none')}</li>
    {/each}
  </ul>
  <p class="hint">{t('palette.hint')}</p>
</dialog>

<style>
  dialog {
    width: min(620px, calc(100vw - 32px));
    margin-top: 12vh;
    padding: 0;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    overflow: hidden;
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  input {
    width: 100%;
    font: inherit;
    font-size: 16px;
    color: var(--text);
    background: var(--surface);
    border: 0;
    border-bottom: 1px solid var(--border);
    padding: var(--space-3) var(--space-4);
    outline: none;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: var(--space-1);
    max-height: min(420px, 60vh);
    overflow: auto;
  }
  li {
    display: flex;
    align-items: baseline;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius-s);
    cursor: pointer;
    font-size: 14px;
    min-width: 0;
  }
  li.active {
    background: var(--accent-soft);
  }
  .title {
    white-space: nowrap;
  }
  .detail {
    flex: 1;
    min-width: 0;
    color: var(--text-muted);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .kind {
    margin-inline-start: auto;
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
  }
  .empty {
    color: var(--text-muted);
    cursor: default;
  }
  .hint {
    margin: 0;
    padding: var(--space-2) var(--space-4);
    border-top: 1px solid var(--border);
    font-size: 12px;
    color: var(--text-muted);
  }
</style>

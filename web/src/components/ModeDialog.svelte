<script lang="ts">
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import type { App } from '../lib/apps';
  import type { Snapshot } from '../lib/docker';
  import { t } from '../lib/i18n';
  import {
    KINDS,
    autoAllowed,
    canStop,
    candidates,
    doKey,
    isDo,
    kindKey,
    listed,
    settingItems,
    splitSettings,
    type Connection,
    type Do,
    type Draft,
    type Item,
    type Kind,
    type Setting,
    type Sources,
  } from '../lib/modes';
  import type { ServicesMsg, Unit } from '../lib/services';
  import type { StackInfo } from '../lib/stacks';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Creates or edits a mode through the profile engine, which writes its
  // file (ROADMAP 10.2). Triggers other than the game are written by hand
  // and armed from the list. Native modal <dialog>: focus is trapped and
  // Esc cancels.
  let { draft, live, onsaved, oncancel }: { draft: Draft | null; live: Live; onsaved: () => void; oncancel: () => void } = $props();

  let dialog: HTMLDialogElement;
  let name = $state('');
  let items = $state<Item[]>([]);
  let settings = $state<Partial<Record<Setting, string>>>({});
  let auto = $state(false);
  let saving = $state(false);

  let kind = $state<Kind>('unit');
  let target = $state('');
  let what = $state<Do>('stop');
  let stackName = $state('');
  let stackDo = $state<Do>('stop');

  let units = $state.raw<Unit[]>([]);
  let docker = $state.raw<Snapshot | null>(null);
  let apps = $state.raw<App[]>([]);
  let stacks = $state.raw<string[]>([]);
  let connections = $state.raw<Connection[]>([]);
  let powers = $state.raw<string[]>([]);

  $effect(() => {
    if (draft && !dialog.open) {
      const split = splitSettings(draft.items);
      name = draft.name;
      items = split.rest;
      settings = split.settings;
      auto = draft.auto_game;
      target = '';
      stackName = '';
      saving = false;
      dialog.showModal();
      api<Snapshot>('/docker').then((s) => (docker = s), () => {});
      api<{ apps: App[] }>('/apps').then((r) => (apps = r.apps), () => {});
      api<StackInfo[]>('/stacks').then((r) => (stacks = r.map((s) => s.name)), () => {});
      api<{ connections: Connection[] }>('/system/network').then((r) => (connections = r.connections ?? []), () => {});
      api<{ profile?: { available: string[] } }>('/power').then((r) => (powers = r.profile?.available ?? []), () => {});
    } else if (!draft && dialog.open) {
      dialog.close();
    }
  });

  $effect(() => {
    if (!draft) return;
    return live.subscribe('services', (data) => {
      const m = data as ServicesMsg;
      if (m?.kind === 'list' && Array.isArray(m.units)) units = m.units;
    });
  });

  $effect(() => {
    if (!canStop(kind)) what = 'start';
  });

  const src = $derived<Sources>({ units, docker, apps, stacks, connections });
  const options = $derived(candidates(kind, src, auto));
  const stackOptions = $derived(candidates('stack', src, auto).filter((c) => !listed(items, { kind: 'stack', target: c.value })));
  const trimmed = $derived(target.trim());
  const managed = $derived(items.filter((i) => i.kind === 'stack'));
  const others = $derived(items.filter((i) => i.kind !== 'stack'));
  const refused = $derived(auto ? items.filter((i) => !autoAllowed(i)) : []);
  const canAdd = $derived(trimmed !== '' && !listed(items, { kind, target: trimmed }) && (!auto || autoAllowed({ kind, target: trimmed })));
  const all = $derived([...settingItems(settings), ...items]);
  const brightnessBad = $derived(settings.brightness !== undefined && settings.brightness !== '' && !/^(100|[1-9][0-9]?)$/.test(settings.brightness));
  const label = (it: Item): string => (it.kind === 'connection' ? (connections.find((c) => c.uuid === it.target)?.name ?? it.target) : it.target);

  function add(): void {
    if (!canAdd) return;
    items = [...items, { kind, target: trimmed, do: what }];
    target = '';
  }

  function addStack(): void {
    if (stackName === '' || listed(items, { kind: 'stack', target: stackName })) return;
    items = [...items, { kind: 'stack', target: stackName, do: stackDo }];
    stackName = '';
  }

  function setDo(it: Item, d: Do): void {
    items = items.map((x) => (x === it ? { ...x, do: d } : x));
  }

  async function save(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (!draft) return;
    saving = true;
    try {
      const body = JSON.stringify({ name: name.trim(), items: all, auto_game: auto });
      await api(draft.id ? `/profiles/${draft.id}` : '/profiles', { method: draft.id ? 'PUT' : 'POST', body });
      onsaved();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
      saving = false;
    }
  }
</script>

{#snippet row(it: Item)}
  <li class:bad={refused.includes(it)}>
    <span class="kind">{t(kindKey(it.kind))}</span>
    <span class="target">{label(it)}</span>
    {#if canStop(it.kind) && isDo(it.do)}
      <select value={it.do} aria-label={t('modes.do')} onchange={(e) => setDo(it, e.currentTarget.value as Do)}>
        <option value="start">{t(doKey('start', it.kind))}</option>
        <option value="stop">{t(doKey('stop', it.kind))}</option>
      </select>
    {:else}
      <span class="kind">{t(doKey('start', it.kind))}</span>
    {/if}
    <button type="button" class="btn" onclick={() => (items = items.filter((x) => x !== it))}>{t('modes.remove')}</button>
  </li>
{/snippet}

<dialog bind:this={dialog} aria-labelledby="mode-title" oncancel={(e) => { e.preventDefault(); oncancel(); }}>
  {#if draft}
    <form onsubmit={save}>
      <h2 id="mode-title">{draft.id ? t('modes.edit') : t('modes.new')}</h2>
      <label class="field">
        <span>{t('modes.name')}</span>
        <input type="text" bind:value={name} maxlength="40" required />
      </label>

      <fieldset>
        <legend>{t('modes.section.session')}</legend>
        <div class="grid">
          <label class="field">
            <span>{t('modes.setting.power-profile')}</span>
            <select bind:value={settings['power-profile']}>
              <option value={undefined}>{t('modes.setting.leave')}</option>
              {#each powers as p (p)}<option value={p}>{p}</option>{/each}
              {#if settings['power-profile'] && !powers.includes(settings['power-profile'])}<option value={settings['power-profile']}>{settings['power-profile']}</option>{/if}
            </select>
          </label>
          <label class="field">
            <span>{t('modes.setting.dnd')}</span>
            <select bind:value={settings.dnd}>
              <option value={undefined}>{t('modes.setting.leave')}</option>
              <option value="on">{t('modes.setting.on')}</option>
              <option value="off">{t('modes.setting.off')}</option>
            </select>
          </label>
          <label class="field">
            <span>{t('modes.setting.brightness')}</span>
            <input type="text" inputmode="numeric" bind:value={settings.brightness} maxlength="3" aria-invalid={brightnessBad} aria-describedby="mode-brightness-hint" />
            <small id="mode-brightness-hint" class:error={brightnessBad}>{t('modes.setting.brightnessHint')}</small>
          </label>
          <label class="field wide">
            <span>{t('modes.setting.wallpaper')}</span>
            <input type="text" bind:value={settings.wallpaper} maxlength="4096" dir="ltr" aria-describedby="mode-wallpaper-hint" />
            <small id="mode-wallpaper-hint">{t('modes.setting.wallpaperHint')}</small>
          </label>
        </div>
      </fieldset>

      <fieldset>
        <legend>{t('modes.section.stacks')}</legend>
        {#if managed.length > 0}
          <ul>{#each managed as it (it.target)}{@render row(it)}{/each}</ul>
        {/if}
        <div class="row">
          <label class="field">
            <span>{t('modes.target')}</span>
            <select bind:value={stackName}>
              <option value="">—</option>
              {#each stackOptions as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
            </select>
          </label>
          <label class="field small">
            <span>{t('modes.do')}</span>
            <select bind:value={stackDo}>
              <option value="start">{t(doKey('start'))}</option>
              <option value="stop">{t(doKey('stop'))}</option>
            </select>
          </label>
          <button type="button" class="btn" onclick={addStack} disabled={stackName === ''}>{t('modes.add')}</button>
        </div>
      </fieldset>

      <fieldset>
        <legend>{t('modes.section.other')}</legend>
        {#if others.length > 0}
          <ul>{#each others as it (it.kind + it.target)}{@render row(it)}{/each}</ul>
        {/if}
        <div class="row">
          <label class="field small">
            <span>{t('modes.kind')}</span>
            <select bind:value={kind}>
              {#each KINDS as k (k)}<option value={k}>{t(kindKey(k))}</option>{/each}
            </select>
          </label>
          <label class="field">
            <span>{t('modes.target')}</span>
            <input type="text" list="mode-targets" bind:value={target} onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } }} />
            <datalist id="mode-targets">
              {#each options as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
            </datalist>
          </label>
          {#if canStop(kind)}
            <label class="field small">
              <span>{t('modes.do')}</span>
              <select bind:value={what}>
                <option value="start">{t(doKey('start', kind))}</option>
                <option value="stop">{t(doKey('stop', kind))}</option>
              </select>
            </label>
          {/if}
          <button type="button" class="btn" onclick={add} disabled={!canAdd}>{t('modes.add')}</button>
        </div>
        {#if kind === 'app'}<p class="note">{t('modes.app.hint')}</p>{/if}
      </fieldset>

      <fieldset>
        <legend>{t('modes.section.triggers')}</legend>
        <label class="toggle"><input type="checkbox" bind:checked={auto} /> {t('modes.auto')}</label>
        {#if auto}<p class="note">{t('modes.auto.hint')}</p>{/if}
        <p class="note">{t('modes.triggers.file')}</p>
      </fieldset>
      {#if refused.length > 0}<p class="note error" role="alert">{t('modes.auto.refused')}</p>{/if}

      <div class="actions">
        <button type="button" class="btn" onclick={oncancel}>{t('modes.cancel')}</button>
        <button type="submit" class="btn primary" disabled={saving || name.trim() === '' || all.length === 0 || refused.length > 0 || brightnessBad}>{t('modes.save')}</button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  dialog {
    width: min(680px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    background: var(--surface);
    color: var(--text);
    padding: var(--space-4);
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 40%);
  }
  h2 {
    font-size: 16px;
    margin: 0 0 var(--space-3);
  }
  fieldset {
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    margin: var(--space-3) 0 0;
    padding: var(--space-2) var(--space-3) var(--space-3);
    min-width: 0;
  }
  legend {
    font-size: 13px;
    font-weight: 600;
    padding: 0 var(--space-1);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 180px), 1fr));
    gap: var(--space-2);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 13px;
    color: var(--text-muted);
    flex: 1 1 200px;
    min-width: 0;
  }
  .field.wide {
    grid-column: 1 / -1;
  }
  .field.small {
    flex: 0 1 130px;
  }
  small {
    font-size: 12px;
  }
  input[type='text'],
  select {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
    min-width: 0;
  }
  .toggle {
    display: flex;
    gap: var(--space-2);
    align-items: center;
    font-size: 13px;
  }
  .note {
    color: var(--text-muted);
    font-size: 12px;
    margin: var(--space-1) 0 0;
  }
  .error {
    color: var(--crit);
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
    max-height: 200px;
    overflow: auto;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: center;
    padding: var(--space-1) 0;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
  }
  li.bad .target {
    color: var(--crit);
  }
  .kind {
    color: var(--text-muted);
    font-size: 12px;
  }
  .target {
    flex: 1 1 10em;
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: flex-end;
    margin-top: var(--space-2);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    margin-top: var(--space-4);
  }
  .btn {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    white-space: nowrap;
  }
  .btn.primary {
    color: var(--accent-contrast);
    background: var(--accent);
    border-color: var(--accent);
  }
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
</style>

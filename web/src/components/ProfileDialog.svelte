<script lang="ts">
  import { api, ApiError } from '../lib/api';
  import type { App } from '../lib/apps';
  import type { Snapshot } from '../lib/docker';
  import { t } from '../lib/i18n';
  import { KINDS, canStop, candidates, doKey, kindKey, listed, unattended, type Do, type Draft, type Item, type Kind, type Profile } from '../lib/profiles';
  import type { ServicesMsg, Unit } from '../lib/services';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Creates or edits a profile. Native modal <dialog>: focus is trapped
  // and Esc cancels.
  let { draft, live, onsaved, oncancel }: { draft: Draft | null; live: Live; onsaved: () => void; oncancel: () => void } = $props();

  let dialog: HTMLDialogElement;
  let name = $state('');
  let items = $state<Item[]>([]);
  let auto = $state(false);
  let saving = $state(false);

  let kind = $state<Kind>('unit');
  let target = $state('');
  let what = $state<Do>('stop');

  let units = $state.raw<Unit[]>([]);
  let docker = $state.raw<Snapshot | null>(null);
  let apps = $state.raw<App[]>([]);

  $effect(() => {
    if (draft && !dialog.open) {
      name = draft.name;
      items = [...draft.items];
      auto = draft.auto_game;
      target = '';
      saving = false;
      dialog.showModal();
      api<Snapshot>('/docker').then((s) => (docker = s), () => {});
      api<{ apps: App[] }>('/apps').then((r) => (apps = r.apps), () => {});
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

  const options = $derived(candidates(kind, units, docker, apps, auto));
  const trimmed = $derived(target.trim());
  const refused = $derived(auto ? items.filter((i) => !unattended(i)) : []);
  const canAdd = $derived(trimmed !== '' && !listed(items, { kind, target: trimmed }) && (!auto || unattended({ kind, target: trimmed })));

  function add(): void {
    if (!canAdd) return;
    items = [...items, { kind, target: trimmed, do: what }];
    target = '';
  }

  function setDo(it: Item, d: Do): void {
    items = items.map((x) => (x === it ? { ...x, do: d } : x));
  }

  async function save(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (!draft) return;
    saving = true;
    try {
      const body = JSON.stringify({ name: name.trim(), items, auto_game: auto });
      await api<Profile>(draft.id ? `/profiles/${draft.id}` : '/profiles', { method: draft.id ? 'PUT' : 'POST', body });
      onsaved();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
      saving = false;
    }
  }
</script>

<dialog bind:this={dialog} aria-labelledby="profile-title" oncancel={(e) => { e.preventDefault(); oncancel(); }}>
  {#if draft}
    <form onsubmit={save}>
      <h2 id="profile-title">{draft.id ? t('profiles.edit') : t('profiles.new')}</h2>
      <label class="field">
        <span>{t('profiles.name')}</span>
        <input type="text" bind:value={name} maxlength="40" required />
      </label>
      <label class="toggle"><input type="checkbox" bind:checked={auto} /> {t('profiles.auto')}</label>
      {#if auto}<p class="note">{t('profiles.auto.hint')}</p>{/if}

      {#if items.length > 0}
        <ul>
          {#each items as it (it.kind + it.target)}
            <li class:bad={refused.includes(it)}>
              <span class="kind">{t(kindKey(it.kind))}</span>
              <span class="target">{it.target}</span>
              {#if canStop(it.kind)}
                <select value={it.do} aria-label={t('profiles.do')} onchange={(e) => setDo(it, e.currentTarget.value as Do)}>
                  <option value="start">{t(doKey('start'))}</option>
                  <option value="stop">{t(doKey('stop'))}</option>
                </select>
              {:else}
                <span class="kind">{t(doKey('start'))}</span>
              {/if}
              <button type="button" class="btn" onclick={() => (items = items.filter((x) => x !== it))}>{t('profiles.remove')}</button>
            </li>
          {/each}
        </ul>
      {/if}
      {#if refused.length > 0}<p class="note error" role="alert">{t('profiles.auto.refused')}</p>{/if}

      <div class="row">
        <label class="field small">
          <span>{t('profiles.kind')}</span>
          <select bind:value={kind}>
            {#each KINDS as k (k)}<option value={k}>{t(kindKey(k))}</option>{/each}
          </select>
        </label>
        <label class="field">
          <span>{t('profiles.target')}</span>
          <input type="text" list="profile-targets" bind:value={target} onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } }} />
          <datalist id="profile-targets">
            {#each options as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </datalist>
        </label>
        {#if canStop(kind)}
          <label class="field small">
            <span>{t('profiles.do')}</span>
            <select bind:value={what}>
              <option value="start">{t(doKey('start'))}</option>
              <option value="stop">{t(doKey('stop'))}</option>
            </select>
          </label>
        {/if}
        <button type="button" class="btn" onclick={add} disabled={!canAdd}>{t('profiles.add')}</button>
      </div>
      {#if kind === 'app'}<p class="note">{t('profiles.app.hint')}</p>{/if}

      <div class="actions">
        <button type="button" class="btn" onclick={oncancel}>{t('profiles.cancel')}</button>
        <button type="submit" class="btn primary" disabled={saving || name.trim() === '' || items.length === 0 || refused.length > 0}>{t('profiles.save')}</button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  dialog {
    width: min(640px, calc(100vw - 32px));
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
  .field {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 13px;
    color: var(--text-muted);
    flex: 1 1 200px;
    min-width: 0;
  }
  .field.small {
    flex: 0 1 130px;
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
    margin-top: var(--space-3);
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
    margin: var(--space-3) 0 0;
    max-height: 240px;
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
    margin-top: var(--space-3);
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

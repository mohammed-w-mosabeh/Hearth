<script lang="ts">
  import Icon from './Icon.svelte';
  import { api, ApiError } from '../lib/api';
  import { BUILTIN_ICONS, builtinName, urlProblem, type TileForm } from '../lib/homepage';
  import { t } from '../lib/i18n';

  // The tile form (SPEC 6): nothing is added or changed until it is saved.
  // An uploaded icon is checked (and an SVG sanitized) by hearthd first.
  let {
    form,
    title,
    groups,
    containers,
    iconData,
    onsave,
    oncancel,
  }: {
    form: TileForm | null;
    title: string;
    groups: string[];
    containers: string[];
    iconData?: string;
    onsave: (f: TileForm) => Promise<string | null>; // an error to show, or null
    oncancel: () => void;
  } = $props();

  let dialog: HTMLDialogElement;
  let f = $state<TileForm>({ name: '', url: '', description: '', icon: '', group: '', container: '', health_check: true });
  let uploaded = $state(''); // data: URI to show
  let uploadedIcon = $state(''); // its "upload:<sha>"
  let error = $state('');
  let busy = $state(false);
  let touchedURL = $state(false);

  $effect(() => {
    if (form && !dialog.open) {
      f = { ...form };
      uploaded = iconData ?? '';
      uploadedIcon = form.icon.startsWith('upload:') ? form.icon : '';
      error = '';
      busy = false;
      touchedURL = false;
      dialog.showModal();
    } else if (!form && dialog.open) {
      dialog.close();
    }
  });

  const problem = $derived(urlProblem(f.url));
  const problemText = $derived(
    problem === 'credentials' ? t('tile.err.credentials') : problem === 'scheme' ? t('tile.err.scheme') : problem === 'empty' ? t('tile.err.empty') : '',
  );
  const ready = $derived(f.name.trim() !== '' && problem === null && !busy);

  async function upload(e: Event): Promise<void> {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file) return;
    if (file.size > 512 * 1024) {
      error = t('tile.err.iconSize');
      return;
    }
    try {
      const r = await api<{ icon: string; data: string }>('/homepage/icons', { method: 'POST', body: await file.arrayBuffer() });
      f.icon = r.icon;
      uploadedIcon = r.icon;
      uploaded = r.data;
      error = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : t('action.failed');
    }
  }

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    touchedURL = true;
    if (!ready) return;
    busy = true;
    const msg = await onsave({ ...f });
    busy = false;
    if (msg) error = msg;
  }
</script>

<dialog bind:this={dialog} aria-labelledby="tile-title" onclose={() => form && oncancel()}>
  {#if form}
    <form onsubmit={submit} novalidate>
      <h2 id="tile-title">{title}</h2>
      <label>
        {t('tile.name')}
        <!-- svelte-ignore a11y_autofocus -->
        <input type="text" bind:value={f.name} maxlength="80" required autofocus />
      </label>
      <label>
        {t('tile.url')}
        <input
          type="url"
          bind:value={f.url}
          placeholder="http://192.168.1.10:8096/"
          spellcheck="false"
          autocomplete="off"
          required
          aria-invalid={touchedURL && problem !== null}
          onblur={() => (touchedURL = true)}
        />
        {#if touchedURL && problemText}<span class="err">{problemText}</span>{/if}
      </label>
      <label>
        {t('tile.description')}
        <input type="text" bind:value={f.description} maxlength="200" />
      </label>
      <div class="row">
        <label>
          {t('tile.group')}
          <input type="text" bind:value={f.group} maxlength="40" list="tile-groups" />
          <datalist id="tile-groups">{#each groups as g (g)}<option value={g}></option>{/each}</datalist>
        </label>
        <label>
          {t('tile.container')}
          <select bind:value={f.container}>
            <option value="">{t('tile.noContainer')}</option>
            {#each containers as c (c)}<option value={c}>{c}</option>{/each}
            {#if f.container && !containers.includes(f.container)}<option value={f.container}>{f.container}</option>{/if}
          </select>
        </label>
      </div>

      <fieldset>
        <legend>{t('tile.icon')}</legend>
        <div class="icons">
          <button type="button" class="ic" class:on={f.icon === ''} aria-pressed={f.icon === ''} title={t('tile.noIcon')} onclick={() => (f.icon = '')}>
            <span class="letter">{(f.name.trim()[0] ?? '?').toUpperCase()}</span>
          </button>
          {#each BUILTIN_ICONS as name (name)}
            <button type="button" class="ic" class:on={builtinName(f.icon) === name} aria-pressed={builtinName(f.icon) === name} title={name} onclick={() => (f.icon = `builtin:${name}`)}>
              <Icon {name} />
            </button>
          {/each}
          {#if uploaded}
            <button type="button" class="ic" class:on={f.icon.startsWith('upload:')} aria-pressed={f.icon.startsWith('upload:')} title={t('tile.uploaded')} onclick={() => (f.icon = uploadedIcon)}>
              <img src={uploaded} alt="" width="20" height="20" />
            </button>
          {/if}
        </div>
        <label class="upload">
          {t('tile.upload')}
          <input type="file" accept="image/png,image/webp,image/svg+xml,.svg" onchange={upload} />
        </label>
      </fieldset>

      <label class="check">
        <input type="checkbox" bind:checked={f.health_check} />
        {t('tile.health')}
      </label>
      <p class="hint">{t('tile.healthHint')}</p>

      {#if error}<p class="err" role="alert">{error}</p>{/if}
      <div class="buttons">
        <button type="button" onclick={() => oncancel()}>{t('confirm.cancel')}</button>
        <button type="submit" class="primary" disabled={!ready}>{t('tile.save')}</button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  dialog {
    width: min(560px, calc(100vw - 32px));
    padding: var(--space-5);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  h2 {
    font-size: 17px;
    margin-bottom: var(--space-3);
  }
  label {
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    margin-bottom: var(--space-3);
  }
  .row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }
  input[type='text'],
  input[type='url'],
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  input[aria-invalid='true'] {
    border-color: var(--crit);
  }
  fieldset {
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-2) var(--space-3);
    margin: 0 0 var(--space-3);
  }
  legend {
    font-size: 13px;
    padding: 0 var(--space-1);
  }
  .icons {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    margin-bottom: var(--space-2);
  }
  .ic {
    display: grid;
    place-items: center;
    width: 34px;
    height: 34px;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    cursor: pointer;
  }
  .ic.on {
    border-color: var(--accent);
    box-shadow: 0 0 0 1px var(--accent);
  }
  .letter {
    font-weight: 600;
  }
  .upload {
    margin: 0;
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-1);
  }
  .hint {
    color: var(--text-muted);
    font-size: 12px;
    margin: 0 0 var(--space-3);
  }
  .err {
    color: var(--crit);
    font-size: 12px;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
  }
  .buttons button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-4);
    cursor: pointer;
  }
  .buttons .primary {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
  .buttons button:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>

<script lang="ts">
  import { t } from '../lib/i18n';
  import type { CapField, Params } from '../lib/actions';

  // A small form of limits; the server validates every value and its
  // refusal shows here. Empty fields are left out of what is saved.
  let {
    open,
    title,
    intro,
    fields,
    error,
    onsave,
    oncancel,
  }: {
    open: boolean;
    title: string;
    intro: string;
    fields: CapField[];
    error: string;
    onsave: (p: Params) => void;
    oncancel: () => void;
  } = $props();

  let dialog: HTMLDialogElement;
  let values = $state<Record<string, string>>({});

  $effect(() => {
    if (open && !dialog.open) {
      values = {};
      dialog.showModal();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  });

  function submit(e: SubmitEvent): void {
    e.preventDefault();
    const params: Params = {};
    for (const f of fields) {
      const v = (values[f.key] ?? '').trim();
      if (v) params[f.key] = v;
    }
    onsave(params);
  }
</script>

<dialog bind:this={dialog} aria-labelledby="cap-title" onclose={() => open && oncancel()}>
  {#if open}
    <form method="dialog" onsubmit={submit}>
      <h2 id="cap-title">{title}</h2>
      <p class="muted">{intro}</p>
      {#each fields as f (f.key)}
        <label>
          {t(f.label)}
          {#if f.options}
            <select bind:value={values[f.key]}>
              <option value={undefined}>{f.none ? t(f.none) : ''}</option>
              {#each f.options as o (o)}<option value={o}>{o}</option>{/each}
            </select>
          {:else}
            <input type="text" dir="ltr" maxlength="20" bind:value={values[f.key]} />
          {/if}
        </label>
      {/each}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="buttons">
        <button type="button" onclick={oncancel}>{t('cap.cancel')}</button>
        <button type="submit" class="primary">{t('cap.save')}</button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  dialog {
    inline-size: min(460px, calc(100vw - 32px));
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
  form {
    display: grid;
    gap: var(--space-3);
  }
  h2 {
    font-size: 17px;
    overflow-wrap: anywhere;
  }
  label {
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    color: var(--text-muted);
  }
  input,
  select {
    font: inherit;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  .muted {
    font-size: 13px;
    color: var(--text-muted);
  }
  .error {
    font-size: 13px;
    color: var(--crit);
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
  }
  button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  button.primary {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-contrast);
  }
</style>

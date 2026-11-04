<script lang="ts">
  import Skeleton from './Skeleton.svelte';
  import { failure } from '../lib/errors';
  import { t, type MessageKey } from '../lib/i18n';
  import { loadOverride, overrideFields, restartPolicies, type Unit } from '../lib/services';
  import type { Params } from '../lib/actions';

  // A unit's limits, saved as a drop-in (internal/dropin). unit null closes
  // the dialog. The server validates every value; its refusal shows here.
  let {
    unit,
    error,
    onsave,
    oncancel,
  }: {
    unit: Unit | null;
    error: string;
    onsave: (p: Params) => void;
    oncancel: () => void;
  } = $props();

  let dialog: HTMLDialogElement;
  let values = $state<Record<string, string>>({});
  let loading = $state(false);
  let loadError = $state('');

  const label = (f: string) => `svc.limits.${f}` as MessageKey;

  $effect(() => {
    if (unit && !dialog.open) {
      const u = unit;
      values = {};
      loadError = '';
      loading = true;
      dialog.showModal();
      loadOverride(u)
        .then((d) => (values = { ...d }))
        .catch((err: unknown) => (loadError = failure(err, 'action.failed')))
        .finally(() => (loading = false));
    } else if (!unit && dialog.open) {
      dialog.close();
    }
  });

  function submit(e: SubmitEvent): void {
    e.preventDefault();
    const params: Params = {};
    for (const f of overrideFields) {
      const v = (values[f] ?? '').trim();
      if (v) params[f] = v;
    }
    onsave(params);
  }
</script>

<dialog bind:this={dialog} aria-labelledby="limits-title" onclose={() => unit && oncancel()}>
  {#if unit}
    <form method="dialog" onsubmit={submit}>
      <h2 id="limits-title">{t('svc.limits.title', { name: unit.name })}</h2>
      <p class="muted">{t('svc.limits.intro')}</p>
      {#if loading}
        <Skeleton label={t('svc.limits.loading')} rows={2} />
      {:else if loadError}
        <p class="error" role="alert">{t('svc.limits.loadError', { msg: loadError })}</p>
      {:else}
        {#each overrideFields as f (f)}
          <label>
            {t(label(f))}
            {#if f === 'Restart'}
              <select bind:value={values[f]}>
                <option value={undefined}>{t('svc.limits.default')}</option>
                {#each restartPolicies as r (r)}<option value={r}>{r}</option>{/each}
              </select>
            {:else}
              <input type="text" dir="ltr" maxlength="20" bind:value={values[f]} />
            {/if}
          </label>
        {/each}
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="buttons">
        <button type="button" onclick={oncancel}>{t('svc.limits.cancel')}</button>
        <button type="submit" class="primary" disabled={loading || loadError !== ''}>{t('svc.limits.save')}</button>
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

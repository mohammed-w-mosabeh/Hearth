<script lang="ts">
  import { reasonKey, type Plan } from '../lib/actions';
  import { t } from '../lib/i18n';

  // Confirmation for destructive actions: shows the previewed plan (what
  // will be affected, what is left alone and why) and, for high-impact
  // actions, asks for the name to be typed. A native modal <dialog>: focus
  // is trapped and Esc cancels. body replaces the default "affects N
  // processes" line; warning is shown above it, highlighted.
  let {
    plan,
    title,
    verb,
    body,
    warning,
    onconfirm,
    oncancel,
  }: {
    plan: Plan | null;
    title: string;
    verb: string;
    body?: string;
    warning?: string;
    onconfirm: (typed: string) => void;
    oncancel: () => void;
  } = $props();

  const SHOW = 8;
  let dialog: HTMLDialogElement;
  let typed = $state('');
  let busy = $state(false);

  $effect(() => {
    if (plan && !dialog.open) {
      typed = '';
      busy = false;
      dialog.showModal();
    } else if (!plan && dialog.open) {
      dialog.close();
    }
  });

  const ready = $derived(plan !== null && plan.items.length > 0 && (!plan.confirm || typed === plan.confirm));
</script>

<dialog
  bind:this={dialog}
  aria-labelledby="confirm-title"
  onclose={() => {
    if (plan) oncancel();
  }}
>
  {#if plan}
    <form
      method="dialog"
      onsubmit={(e) => {
        e.preventDefault();
        if (!ready || busy) return;
        busy = true;
        onconfirm(typed);
      }}
    >
      <h2 id="confirm-title">{title}</h2>
      {#if warning}<p class="warning" role="alert">{warning}</p>{/if}
      <p>{body ?? t('confirm.affects', { n: plan.items.length })}</p>
      <ul class="items">
        {#each plan.items.slice(0, SHOW) as it (it.id)}
          <li><span class="name">{it.name}</span> <span class="muted">{it.id.startsWith('pid:') ? it.id.split(':')[1] : (it.detail ?? '')}</span></li>
        {/each}
        {#if plan.items.length > SHOW}
          <li class="muted">{t('apps.more', { n: plan.items.length - SHOW })}</li>
        {/if}
      </ul>
      {#if plan.blocked.length > 0}
        <details>
          <summary>{t('confirm.skipped', { n: plan.blocked.length })}</summary>
          <ul class="items">
            {#each plan.blocked as b (b.id)}
              <li><span class="name">{b.name}</span> <span class="muted">{t(reasonKey(b.reason), { user: b.detail ?? '', detail: b.detail ?? '' })}</span></li>
            {/each}
          </ul>
        </details>
      {/if}
      {#if plan.confirm}
        <label class="typed">
          {t('confirm.type', { name: plan.confirm })}
          <!-- svelte-ignore a11y_autofocus -->
          <input type="text" bind:value={typed} autocomplete="off" spellcheck="false" autofocus />
        </label>
      {/if}
      <div class="buttons">
        <button type="button" onclick={() => oncancel()}>{t('confirm.cancel')}</button>
        <button type="submit" class="danger" disabled={!ready || busy}>{verb}</button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  dialog {
    width: min(480px, calc(100vw - 32px));
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
    margin-bottom: var(--space-2);
  }
  p {
    margin: 0 0 var(--space-2);
    color: var(--text-muted);
  }
  .warning {
    color: var(--crit);
    border-inline-start: 3px solid var(--crit);
    padding-inline-start: var(--space-2);
  }
  .items {
    list-style: none;
    margin: 0 0 var(--space-3);
    padding: 0;
    font-size: 13px;
    max-height: 200px;
    overflow: auto;
  }
  .items li {
    padding: 2px 0;
  }
  .muted {
    color: var(--text-muted);
    font-size: 12px;
  }
  details {
    font-size: 13px;
    margin-bottom: var(--space-3);
    color: var(--text-muted);
  }
  summary {
    cursor: pointer;
    margin-bottom: var(--space-1);
  }
  .typed {
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    margin-bottom: var(--space-3);
  }
  input {
    font: inherit;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
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
  button.danger {
    background: var(--crit);
    border-color: var(--crit);
    color: #fff;
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>

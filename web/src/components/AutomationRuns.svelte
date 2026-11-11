<script lang="ts">
  import Skeleton from './Skeleton.svelte';
  import { api } from '../lib/api';
  import { reasonKey, runStateKey, stepResultKey, stepWords, triggerWords, type Automation, type Run } from '../lib/automations';
  import { failure } from '../lib/errors';
  import { locale, t } from '../lib/i18n';

  // An automation's runs (ROADMAP 12.9): what fired it, whether its
  // conditions held, and each step's result. Everything is shown as text.
  let { automation, onclose }: { automation: Automation | null; onclose: () => void } = $props();

  let dialog: HTMLDialogElement;
  let runs = $state.raw<Run[] | null>(null);
  let failed = $state('');

  $effect(() => {
    const a = automation;
    if (!a) {
      if (dialog.open) dialog.close();
      return;
    }
    runs = null;
    failed = '';
    if (!dialog.open) dialog.showModal();
    let stopped = false;
    api<{ runs: Run[] }>(`/automations/${a.id}/runs`).then(
      (r) => {
        if (!stopped) runs = r.runs;
      },
      (err) => {
        if (!stopped) failed = failure(err, 'auto.runs.error');
      },
    );
    return () => {
      stopped = true;
    };
  });

  const when = (ms: number) => new Date(ms).toLocaleString(locale());

  function reasonText(reason: string): string {
    const k = reasonKey(reason);
    return k ? t(k) : reason;
  }

  function stepText(s: Parameters<typeof stepWords>[0]): string {
    const w = stepWords(s);
    return t(w.key, w.values);
  }

  function firedBy(r: Run): string {
    if (r.record.by === 'test') return t('auto.runs.byTest');
    if (r.record.trigger?.kind === 'alert' || !r.record.trigger) return t('auto.runs.byTrigger');
    const inst = r.record.trigger.instance ?? '';
    return inst ? t('auto.runs.byInstance', { s: inst }) : t('auto.runs.byTrigger');
  }

  const head = $derived.by(() => {
    const a = automation;
    if (!a?.spec) return '';
    const w = triggerWords(a.spec.trigger);
    return t(w.key, w.values);
  });
</script>

<dialog bind:this={dialog} aria-labelledby="runs-title" onclose={() => automation && onclose()}>
  {#if automation}
    <header>
      <h2 id="runs-title">{t('auto.runs.title', { s: automation.name })}</h2>
      <button type="button" class="x" aria-label={t('auto.close')} onclick={() => onclose()}>×</button>
    </header>
    {#if head}<p class="note">{head}</p>{/if}
    {#if failed}
      <p class="note error" role="status">{failed}</p>
    {:else if runs === null}
      <Skeleton label={t('auto.runs.loading')} rows={2} />
    {:else if runs.length === 0}
      <p class="note">{t('auto.runs.none')}</p>
    {:else}
      <ol class="runs">
        {#each runs as r (r.id)}
          <li class="run {r.state}">
            <div class="line">
              <time datetime={new Date(r.started).toISOString()}>{when(r.started)}</time>
              <span class="state">{t(runStateKey(r.state))}</span>
              <span class="sub">{firedBy(r)}</span>
            </div>
            {#if r.record.reason}<p class="sub">{reasonText(r.record.reason)}</p>{/if}
            {#if r.record.conditions === false}<p class="sub">{t('auto.runs.conditionsFalse')}</p>{/if}
            {#if r.record.values?.length}
              <ul class="values">
                {#each r.record.values as v, i (i)}<li>{v}</li>{/each}
              </ul>
            {/if}
            {#if r.record.steps.length > 0}
              <ol class="steps">
                {#each r.record.steps as s, i (i)}
                  <li class={s.result}>
                    <span class="result">{t(stepResultKey(s.result))}</span>
                    <span>{stepText(s)}</span>
                    {#if s.detail}<span class="sub">{s.detail}</span>{/if}
                  </li>
                {/each}
              </ol>
            {/if}
          </li>
        {/each}
      </ol>
    {/if}
  {/if}
</dialog>

<style>
  dialog {
    width: min(820px, calc(100vw - 32px));
    max-height: min(720px, calc(100vh - 32px));
    padding: var(--space-4);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: var(--space-2);
  }
  h2 {
    font-size: 17px;
    overflow-wrap: anywhere;
  }
  .x {
    font: inherit;
    font-size: 20px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 0;
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
  }
  .note,
  .sub {
    color: var(--text-muted);
    font-size: 13px;
    margin: 0 0 var(--space-2);
    overflow-wrap: anywhere;
  }
  .sub {
    font-size: 12px;
    margin: 0;
  }
  .error {
    color: var(--crit);
  }
  ol,
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .run {
    border-top: 1px solid var(--border);
    padding: var(--space-2) 0;
    font-size: 13px;
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: baseline;
  }
  .state {
    font-weight: 600;
  }
  .run.done .state {
    color: var(--ok);
  }
  .run.failed .state,
  .run.limited .state {
    color: var(--crit);
  }
  .run.waiting .state {
    color: var(--warn);
  }
  .steps,
  .values {
    margin-top: var(--space-1);
    padding-inline-start: var(--space-3);
  }
  .steps li,
  .values li {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    padding: 2px 0;
    overflow-wrap: anywhere;
  }
  .result {
    min-width: 6em;
    color: var(--text-muted);
  }
  .steps li.ok .result {
    color: var(--ok);
  }
  .steps li.failed .result {
    color: var(--crit);
  }
  .steps li.waiting .result {
    color: var(--warn);
  }
</style>

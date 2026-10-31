<script lang="ts">
  import { blankRule, instanceLabel, metricLabel, ruleError, type Metric, type RuleDraft } from '../lib/alerts';
  import { t } from '../lib/i18n';

  // Add or edit an alert rule. draft null closes the dialog.
  let {
    draft,
    editing,
    metrics,
    error,
    onsave,
    oncancel,
  }: {
    draft: RuleDraft | null;
    editing: boolean;
    metrics: Metric[];
    error: string;
    onsave: (r: RuleDraft) => void;
    oncancel: () => void;
  } = $props();

  let dialog: HTMLDialogElement;
  let r = $state<RuleDraft>(blankRule(undefined));
  let minutes = $state(1);
  let quiet = $state(false);
  let tried = $state(false);

  $effect(() => {
    if (draft && !dialog.open) {
      r = { ...draft };
      minutes = draft.for_s / 60;
      quiet = draft.quiet_from !== '';
      tried = false;
      dialog.showModal();
    } else if (!draft && dialog.open) {
      dialog.close();
    }
  });

  const metric = $derived(metrics.find((m) => m.id === r.metric));
  const isState = $derived(metric?.unit === '');
  const result = $derived<RuleDraft>({
    ...r,
    name: r.name.trim(),
    for_s: Math.round(minutes * 60),
    op: isState ? '>' : r.op,
    threshold: isState ? 0 : Number(r.threshold),
    quiet_from: quiet ? r.quiet_from : '',
    quiet_to: quiet ? r.quiet_to : '',
  });
  const problem = $derived(ruleError(result, metric));

  function changeMetric(id: string): void {
    const m = metrics.find((x) => x.id === id);
    r.metric = id;
    r.target = '';
    if (m?.unit === '') r.threshold = 0;
    else if (r.threshold < (m?.min ?? 0) || r.threshold > (m?.max ?? 100) || r.threshold === 0) r.threshold = 90;
    if (id === 'battery.discharging') r.op = '<';
  }
</script>

<dialog bind:this={dialog} aria-labelledby="rule-title" onclose={() => draft && oncancel()}>
  {#if draft}
    <form
      method="dialog"
      onsubmit={(e) => {
        e.preventDefault();
        tried = true;
        if (!problem) onsave(result);
      }}
    >
      <h2 id="rule-title">{t(editing ? 'alerts.form.edit' : 'alerts.form.add')}</h2>
      <label>
        {t('alerts.form.name')}
        <input type="text" bind:value={r.name} maxlength="80" required />
      </label>
      <label>
        {t('alerts.form.metric')}
        <select value={r.metric} onchange={(e) => changeMetric(e.currentTarget.value)}>
          {#each metrics as m (m.id)}
            <option value={m.id}>{metricLabel(m, m.id)}</option>
          {/each}
        </select>
      </label>
      {#if metric?.instance}
        <label>
          {t('alerts.form.target', { what: instanceLabel(metric) })}
          <input
            type="text"
            list="rule-instances"
            bind:value={r.target}
            maxlength={metric.id === 'log.match' ? 200 : 255}
            required={metric.id === 'log.match'}
            placeholder={metric.id === 'log.match' ? '' : t('alerts.form.targetAny', { what: instanceLabel(metric) })}
          />
          <datalist id="rule-instances">
            {#each metric.instances as i (i)}<option value={i}></option>{/each}
          </datalist>
        </label>
        {#if metric.id === 'log.match'}<p class="hint">{t('alerts.form.patternHint')}</p>{/if}
      {/if}
      {#if !isState}
        <div class="row">
          <label>
            {t('alerts.form.when')}
            <select bind:value={r.op}>
              <option value=">">{t('alerts.form.above')}</option>
              <option value="<">{t('alerts.form.below')}</option>
            </select>
          </label>
          <label>
            {t('alerts.form.threshold', { unit: metric?.unit ?? '' })}
            <input type="number" bind:value={r.threshold} min={metric?.min} max={metric?.max} step="1" />
          </label>
          <label>
            {t('alerts.form.for')}
            <input type="number" bind:value={minutes} min="0" max="1440" step="0.5" />
          </label>
        </div>
        <p class="hint">{t('alerts.form.hysteresis', { n: metric?.hysteresis ?? 0, unit: metric?.unit ?? '' })}</p>
      {/if}
      <div class="row">
        <label>
          {t('alerts.form.severity')}
          <select bind:value={r.severity}>
            <option value="info">{t('alerts.sev.info')}</option>
            <option value="warning">{t('alerts.sev.warning')}</option>
            <option value="critical">{t('alerts.sev.critical')}</option>
          </select>
        </label>
        <label class="check"><input type="checkbox" bind:checked={r.enabled} /> {t('alerts.form.enabled')}</label>
      </div>
      <label class="check"><input type="checkbox" bind:checked={quiet} /> {t('alerts.form.quiet')}</label>
      {#if quiet}
        <div class="row">
          <label>{t('alerts.form.quietFrom')} <input type="time" bind:value={r.quiet_from} /></label>
          <label>{t('alerts.form.quietTo')} <input type="time" bind:value={r.quiet_to} /></label>
        </div>
        <p class="hint">{t('alerts.form.quietHint')}</p>
      {/if}
      {#if (tried && problem) || error}<p class="error" role="alert">{error || problem}</p>{/if}
      <div class="buttons">
        <button type="button" onclick={() => oncancel()}>{t('confirm.cancel')}</button>
        <button type="submit" class="primary">{t('alerts.form.save')}</button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  dialog {
    width: min(520px, calc(100vw - 32px));
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
  }
  label {
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    color: var(--text-muted);
  }
  label.check {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text);
  }
  .row {
    display: flex;
    gap: var(--space-3);
    align-items: end;
    flex-wrap: wrap;
  }
  .row label {
    flex: 1;
    min-width: 120px;
  }
  input:not([type='checkbox']),
  select {
    font: inherit;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  .hint {
    margin: calc(-1 * var(--space-2)) 0 0;
    font-size: 12px;
    color: var(--text-muted);
  }
  .error {
    margin: 0;
    color: var(--crit);
    font-size: 13px;
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

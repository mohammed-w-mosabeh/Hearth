<script lang="ts">
  import { api, ApiError } from '../lib/api';
  import { locale, type MessageKey, t } from '../lib/i18n';
  import { DAYS, fromCalendar, jobError, toCalendar, type Builder, type Check, type Day, type Job, type Scope } from '../lib/jobs';
  import { toast } from '../lib/toast.svelte';

  // The job form (SPEC 14): name, who runs it, script, schedule (a visual
  // builder writing systemd OnCalendar, or a delay after boot, or an
  // interval), timeout and failure notification. systemd itself checks
  // the schedule as it is typed and shows the next runs.
  let { edit, onclose, onsaved }: { edit: { scope: Scope; job: Job } | null; onclose: () => void; onsaved: () => void } = $props();

  let dialog: HTMLDialogElement;
  let job = $state<Job | null>(null);
  let scope = $state<Scope>('user');
  let builder = $state<Builder>({ freq: 'daily', time: '03:00' });
  let span = $state('');
  let minutes = $state(60);
  let check = $state<Check | null>(null);
  let saving = $state(false);
  let failed = $state('');
  let rootProblem = $state('');

  $effect(() => {
    if (!edit) {
      if (dialog.open) dialog.close();
      return;
    }
    job = { ...edit.job, schedule: { ...edit.job.schedule } };
    scope = edit.scope;
    builder = edit.job.schedule.kind === 'calendar' ? fromCalendar(edit.job.schedule.value) : { freq: 'daily', time: '03:00' };
    span = edit.job.schedule.kind === 'calendar' ? '1h' : edit.job.schedule.value;
    minutes = Math.max(1, Math.round(edit.job.timeout / 60));
    failed = '';
    check = null;
    if (!dialog.open) dialog.showModal();
  });

  // Root jobs go through hearth-broker: say up front if it cannot be used.
  $effect(() => {
    if (scope !== 'system') {
      rootProblem = '';
      return;
    }
    api<{ available: boolean; member?: boolean; reason?: string }>('/broker/status')
      .then((s) => (rootProblem = s.member ? '' : s.reason === 'not-member' ? t('jobs.root.notMember') : t('jobs.root.noBroker')))
      .catch(() => (rootProblem = t('jobs.root.noBroker')));
  });

  // The schedule the form describes.
  $effect(() => {
    if (!job) return;
    if (job.schedule.kind === 'calendar') job.schedule.value = toCalendar(builder) ?? '';
    else job.schedule.value = span.trim();
  });
  $effect(() => {
    if (job) job.timeout = Math.round(minutes * 60);
  });

  // Ask systemd, a moment after typing stops.
  $effect(() => {
    if (!job) return;
    const { kind, value } = job.schedule;
    if (!value) {
      check = null;
      return;
    }
    let stopped = false;
    const timer = setTimeout(() => {
      api<Check>(`/jobs/check?${new URLSearchParams({ kind, value }).toString()}`)
        .then((c) => {
          if (!stopped) check = c;
        })
        .catch(() => {});
    }, 350);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  });

  const problem = $derived(job ? jobError(job) : null);
  const problemKey: Record<string, MessageKey> = {
    name: 'jobs.err.name',
    script: 'jobs.err.script',
    scriptSize: 'jobs.err.scriptSize',
    timeout: 'jobs.err.timeout',
    schedule: 'jobs.err.schedule',
  };

  function toggleDay(d: Day): void {
    if (builder.freq !== 'weekly') return;
    builder.days = builder.days.includes(d) ? builder.days.filter((x) => x !== d) : [...builder.days, d];
  }

  function setFreq(f: Builder['freq']): void {
    const time = 'time' in builder ? builder.time : '03:00';
    builder =
      f === 'minutes'
        ? { freq: f, every: 15 }
        : f === 'hourly'
          ? { freq: f, minute: 0 }
          : f === 'daily'
            ? { freq: f, time }
            : f === 'weekly'
              ? { freq: f, days: ['Mon'], time }
              : f === 'monthly'
                ? { freq: f, day: 1, time }
                : { freq: f, expr: job?.schedule.value ?? '' };
  }

  async function save(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (!job || problem || !check?.valid) return;
    saving = true;
    failed = '';
    if (scope === 'system') toast(t('jobs.root.authHint'));
    try {
      await api(`/jobs/${scope}`, { method: 'POST', body: JSON.stringify(job) });
      toast(t(job.id ? 'jobs.saved' : 'jobs.created', { name: job.name }));
      onsaved();
      onclose();
    } catch (err) {
      failed = err instanceof ApiError ? err.message : t('jobs.saveFailed');
    } finally {
      saving = false;
    }
  }

  const when = (ms: number) => new Date(ms).toLocaleString(locale('en-GB'), { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
  const freqs: { id: Builder['freq']; label: MessageKey }[] = [
    { id: 'minutes', label: 'jobs.freq.minutes' },
    { id: 'hourly', label: 'jobs.freq.hourly' },
    { id: 'daily', label: 'jobs.freq.daily' },
    { id: 'weekly', label: 'jobs.freq.weekly' },
    { id: 'monthly', label: 'jobs.freq.monthly' },
    { id: 'custom', label: 'jobs.freq.custom' },
  ];
</script>

<dialog bind:this={dialog} aria-labelledby="job-title" onclose={() => edit && onclose()}>
  {#if job}
    <form onsubmit={save}>
      <header>
        <h2 id="job-title">{job.id ? t('jobs.editTitle', { name: edit?.job.name ?? '' }) : t('jobs.newTitle')}</h2>
        <button type="button" class="x" aria-label={t('jobs.cancel')} onclick={() => onclose()}>×</button>
      </header>

      <label class="field">
        <span>{t('jobs.f.name')}</span>
        <input type="text" bind:value={job.name} maxlength="80" required placeholder={t('jobs.f.namePh')} />
      </label>

      <fieldset class="field">
        <legend>{t('jobs.f.runAs')}</legend>
        <label><input type="radio" bind:group={scope} value="user" disabled={!!job.id} /> {t('jobs.f.you')}</label>
        <label><input type="radio" bind:group={scope} value="system" disabled={!!job.id} /> {t('jobs.f.root')}</label>
        {#if scope === 'system'}<p class="hint warn">{rootProblem || t('jobs.root.hint')}</p>{/if}
      </fieldset>

      <fieldset class="field">
        <legend>{t('jobs.f.when')}</legend>
        <div class="row">
          <select bind:value={job.schedule.kind} aria-label={t('jobs.f.kind')}>
            <option value="calendar">{t('jobs.kind.calendar')}</option>
            <option value="interval">{t('jobs.kind.interval')}</option>
            <option value="boot">{t('jobs.kind.boot')}</option>
          </select>
          {#if job.schedule.kind === 'calendar'}
            <select value={builder.freq} onchange={(e) => setFreq(e.currentTarget.value as Builder['freq'])} aria-label={t('jobs.f.freq')}>
              {#each freqs as f (f.id)}<option value={f.id}>{t(f.label)}</option>{/each}
            </select>
            {#if builder.freq === 'minutes'}
              <label class="inline">{t('jobs.f.every')} <input type="number" min="1" max="59" bind:value={builder.every} /> {t('jobs.f.min')}</label>
            {:else if builder.freq === 'hourly'}
              <label class="inline">{t('jobs.f.atMinute')} <input type="number" min="0" max="59" bind:value={builder.minute} /></label>
            {:else if builder.freq === 'custom'}
              <input class="grow mono" type="text" bind:value={builder.expr} placeholder="Mon..Fri 09:30" aria-label={t('jobs.f.expr')} />
            {:else}
              {#if builder.freq === 'monthly'}
                <label class="inline">{t('jobs.f.day')} <input type="number" min="1" max="28" bind:value={builder.day} /></label>
              {/if}
              <label class="inline">{t('jobs.f.at')} <input type="time" bind:value={builder.time} required /></label>
            {/if}
          {:else}
            <input class="grow mono" type="text" bind:value={span} placeholder="6h" aria-label={t('jobs.f.span')} />
          {/if}
        </div>
        {#if job.schedule.kind === 'calendar' && builder.freq === 'weekly'}
          <div class="days" role="group" aria-label={t('jobs.f.days')}>
            {#each DAYS as d (d)}
              <label class:on={builder.days.includes(d)}><input type="checkbox" checked={builder.days.includes(d)} onchange={() => toggleDay(d)} />{d}</label>
            {/each}
          </div>
        {/if}
        <p class="hint" role="status">
          {#if !check}
            &nbsp;
          {:else if !check.valid}
            <span class="error">{check.error}</span>
          {:else if check.next?.length}
            {t('jobs.f.next')} {check.next.slice(0, 3).map(when).join(' · ')} <span class="mono muted">({check.normalized})</span>
          {:else}
            {job.schedule.kind === 'boot' ? t('jobs.f.afterBoot', { span: check.normalized ?? '' }) : t('jobs.f.everySpan', { span: check.normalized ?? '' })}
          {/if}
        </p>
      </fieldset>

      <label class="field">
        <span>{t('jobs.f.script')}</span>
        <textarea class="mono" rows="9" spellcheck="false" bind:value={job.script}></textarea>
        <span class="hint">{scope === 'system' ? t('jobs.f.scriptRoot') : t('jobs.f.scriptUser')}</span>
      </label>

      <div class="row">
        <label class="inline">{t('jobs.f.timeout')} <input type="number" min="1" max="10080" bind:value={minutes} /> {t('jobs.f.min')}</label>
        <label class="inline"><input type="checkbox" bind:checked={job.notify} /> {t('jobs.f.notify')}</label>
      </div>

      {#if failed}<p class="error" role="alert">{failed}</p>{/if}
      <div class="actions">
        {#if problem}<span class="hint error">{t(problemKey[problem] ?? 'jobs.err.name')}</span>{/if}
        <button type="button" class="btn" onclick={() => onclose()}>{t('jobs.cancel')}</button>
        <button type="submit" class="btn primary" disabled={saving || !!problem || !check?.valid || (scope === 'system' && !!rootProblem)}>
          {saving ? t('jobs.saving') : t('jobs.save')}
        </button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  dialog {
    width: min(680px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
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
    margin-bottom: var(--space-3);
  }
  h2 {
    font-size: 17px;
  }
  .x {
    font: inherit;
    font-size: 20px;
    color: var(--text-muted);
    background: none;
    border: 0;
    cursor: pointer;
  }
  .field {
    display: grid;
    gap: var(--space-1);
    margin: 0 0 var(--space-3);
    padding: 0;
    border: 0;
    font-size: 13px;
  }
  .field > span,
  legend {
    font-weight: 600;
    font-size: 12px;
    color: var(--text-muted);
    padding: 0;
    margin-bottom: var(--space-1);
  }
  fieldset label {
    margin-inline-end: var(--space-4);
  }
  input[type='text'],
  input[type='number'],
  input[type='time'],
  select,
  textarea {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  input[type='number'] {
    width: 5em;
  }
  textarea {
    resize: vertical;
    width: 100%;
    box-sizing: border-box;
  }
  .mono {
    font-family: var(--font-mono, monospace);
    font-size: 12px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: center;
    font-size: 13px;
  }
  .grow {
    flex: 1;
    min-width: 160px;
  }
  .inline {
    display: inline-flex;
    gap: var(--space-1);
    align-items: center;
  }
  .days {
    display: flex;
    gap: var(--space-1);
    margin-top: var(--space-2);
  }
  .days label {
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 2px 8px;
    cursor: pointer;
    margin: 0;
  }
  .days label.on {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
  .days input {
    position: absolute;
    opacity: 0;
    width: 1px;
    height: 1px;
  }
  .hint {
    font-size: 12px;
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .muted {
    color: var(--text-muted);
  }
  .warn {
    color: var(--warn);
  }
  .error {
    color: var(--crit);
  }
  .actions {
    display: flex;
    gap: var(--space-2);
    justify-content: flex-end;
    align-items: center;
    margin-top: var(--space-4);
  }
  .actions .hint {
    margin: 0 auto 0 0;
  }
  .btn {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-4);
    cursor: pointer;
  }
  .btn.primary {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>

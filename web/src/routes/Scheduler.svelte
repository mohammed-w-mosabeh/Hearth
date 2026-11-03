<script lang="ts">
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { failure } from '../lib/errors';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import JobDialog from '../components/JobDialog.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { api } from '../lib/api';
  import { duration } from '../lib/format';
  import { locale, menuLeft, type MessageKey, t } from '../lib/i18n';
  import { blank, describe, order, type Job, type Listed, type Run, type Scope } from '../lib/jobs';
  import { toast } from '../lib/toast.svelte';

  // Scheduler (SPEC 14): jobs are real systemd timers, hearth-job-<id>,
  // yours in your user manager and root ones through hearth-broker (which
  // asks for the administrator password). History comes from the journal.
  const REFRESH_MS = 10000;

  let jobs = $state.raw<Listed[] | null>(null);
  let failed = $state('');

  async function load(): Promise<void> {
    try {
      jobs = order((await api<{ jobs: Listed[] }>('/jobs')).jobs);
      failed = '';
    } catch (err) {
      failed = failure(err, 'jobs.error');
    }
  }
  $effect(() => {
    void load();
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') void load();
    }, REFRESH_MS);
    return () => clearInterval(timer);
  });

  let editing = $state<{ scope: Scope; job: Job } | null>(null);

  async function edit(j: Listed): Promise<void> {
    menu.hidePopover();
    if (j.scope === 'system') toast(t('jobs.root.authHint'));
    try {
      editing = { scope: j.scope, job: await api<Job>(`/jobs/${j.scope}/${j.id}`) };
    } catch (err) {
      toast(failure(err, 'jobs.error'), { tone: 'error' });
    }
  }

  // Actions: run, pause, resume, delete (previewed).
  type Op = 'run' | 'pause' | 'resume' | 'delete';
  const done: Record<Op, MessageKey> = { run: 'jobs.done.run', pause: 'jobs.done.pause', resume: 'jobs.done.resume', delete: 'jobs.done.delete' };
  let pending = $state<{ job: Listed; plan: Plan } | null>(null);

  function report(op: Op, j: Listed, res: Result): void {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done === 0 && bad) {
      const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
      toast(t('jobs.failed', { name: j.name, reason }), { tone: 'error' });
      return;
    }
    const undo = op === 'pause' ? () => void act('resume', j) : undefined;
    toast(t(done[op], { name: j.name }), undo ? { undo } : {});
    setTimeout(() => void load(), op === 'run' ? 1500 : 300);
  }

  async function act(op: Op, j: Listed): Promise<void> {
    menu.hidePopover();
    const target = `${j.scope}/${j.id}`;
    if (j.scope === 'system') toast(t('jobs.root.authHint'));
    try {
      if (op === 'delete') {
        const plan = await preview('job', 'delete', target);
        pending = { job: j, plan };
        return;
      }
      report(op, j, await run('job', op, target));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { job: j, plan } = pending;
    pending = null;
    try {
      report('delete', j, await run('job', 'delete', `${j.scope}/${j.id}`, {}, plan.token, typed || undefined));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  // History.
  let historyOf = $state<Listed | null>(null);
  let runs = $state.raw<Run[] | null>(null);
  let limited = $state(false);
  let historyDialog: HTMLDialogElement;
  $effect(() => {
    if (!historyOf) {
      if (historyDialog.open) historyDialog.close();
      return;
    }
    runs = null;
    if (!historyDialog.open) historyDialog.showModal();
    const j = historyOf;
    api<{ runs: Run[]; limited: boolean }>(`/jobs/${j.scope}/${j.id}/history`)
      .then((h) => {
        runs = h.runs;
        limited = h.limited;
      })
      .catch(() => (runs = []));
  });

  let menu: HTMLDivElement;
  let subject = $state<Listed | null>(null);
  function openMenu(e: MouseEvent, j: Listed): void {
    subject = j;
    const b = (e.currentTarget as HTMLElement).getBoundingClientRect();
    menu.showPopover();
    const w = menu.offsetWidth;
    const h = menu.offsetHeight;
    menu.style.top = `${b.bottom + 4 + h > innerHeight ? Math.max(4, b.top - 4 - h) : b.bottom + 4}px`;
    menu.style.left = `${menuLeft(b, w)}px`;
  }

  const when = (ms: number) => new Date(ms).toLocaleString(locale('en-GB'), { dateStyle: 'medium', timeStyle: 'short' });
  function lastText(j: Listed): string {
    if (j.running) return t('jobs.state.running');
    if (!j.last) return t('jobs.never');
    const ok = !j.failed && (j.result === 'success' || j.result === undefined);
    return `${when(j.last)} · ${ok ? t('jobs.ok') : t('jobs.failedExit', { code: j.exit ?? '?', result: j.result ?? '' })}`;
  }
</script>

<header>
  <div>
    <h1>{t('jobs.title')}</h1>
    <p>{jobs ? t('jobs.count', { n: jobs.length }) : t('jobs.intro')}</p>
  </div>
  <button type="button" class="btn primary" onclick={() => (editing = { scope: 'user', job: blank() })}>+ {t('jobs.new')}</button>
</header>
{#if failed}<p class="note error" role="status">{failed}</p>{/if}

{#if jobs === null && !failed}
  <Skeleton label={t('jobs.loading')} />
{:else if jobs && jobs.length === 0}
  <EmptyState inline icon="calendar" body={t('jobs.empty')}>
    <button type="button" class="btn" onclick={() => (editing = { scope: 'user', job: blank() })}>{t('jobs.new')}</button>
  </EmptyState>
{:else if jobs}
  <div class="scroll">
    <table>
      <thead>
        <tr>
          <th>{t('jobs.col.name')}</th>
          <th>{t('jobs.col.schedule')}</th>
          <th>{t('jobs.col.next')}</th>
          <th>{t('jobs.col.last')}</th>
          <th><span class="visually-hidden">{t('jobs.col.actions')}</span></th>
        </tr>
      </thead>
      <tbody>
        {#each jobs as j (j.scope + j.id)}
          <tr class:paused={j.paused}>
            <td class="name">
              <button type="button" class="link" onclick={() => (historyOf = j)}>{j.name}</button>
              {#if j.scope === 'system'}<span class="tag warn" title={t('jobs.rootHint')}>{t('jobs.tag.root')}</span>{/if}
              {#if j.paused}<span class="tag">{t('jobs.tag.paused')}</span>{/if}
            </td>
            <td title={j.schedule.value}>{describe(j.schedule)}</td>
            <td>{j.paused ? '–' : j.next ? when(j.next) : '–'}</td>
            <td class:bad={j.failed}>{lastText(j)}</td>
            <td class="act">
              <button type="button" class="more-btn" aria-label={t('jobs.actionsFor', { name: j.name })} onclick={(e) => openMenu(e, j)}>⋯</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="foot">{t('jobs.footnote')}</p>
{/if}

<div bind:this={menu} popover="auto" class="menu" role="group" aria-label={subject ? t('jobs.actionsFor', { name: subject.name }) : undefined}>
  {#if subject}
    {@const j = subject}
    <button type="button" onclick={() => act('run', j)} disabled={j.running}>{t('jobs.op.run')}</button>
    {#if j.paused}
      <button type="button" onclick={() => act('resume', j)}>{t('jobs.op.resume')}</button>
    {:else}
      <button type="button" onclick={() => act('pause', j)}>{t('jobs.op.pause')}</button>
    {/if}
    <button type="button" onclick={() => edit(j)}>{t('jobs.op.edit')}</button>
    <button
      type="button"
      onclick={() => {
        menu.hidePopover();
        historyOf = j;
      }}>{t('jobs.op.history')}</button
    >
    <hr />
    <button type="button" class="danger" onclick={() => act('delete', j)}>{t('jobs.op.delete')}</button>
  {/if}
</div>

<JobDialog edit={editing} onclose={() => (editing = null)} onsaved={() => void load()} />

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('jobs.confirmDelete', { name: pending.job.name }) : ''}
  verb={t('jobs.op.delete')}
  body={t('jobs.confirmDeleteBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<dialog bind:this={historyDialog} class="history" aria-labelledby="hist-title" onclose={() => historyOf && (historyOf = null)}>
  {#if historyOf}
    <header>
      <h2 id="hist-title">{t('jobs.historyTitle', { name: historyOf.name })}</h2>
      <button type="button" class="x" aria-label={t('jobs.close')} onclick={() => (historyOf = null)}>×</button>
    </header>
    {#if limited}<p class="note warn">{t('jobs.historyLimited')}</p>{/if}
    {#if runs === null}
      <Skeleton label={t('jobs.loading')} />
    {:else if runs.length === 0}
      <p class="note">{t('jobs.noRuns')}</p>
    {:else}
      <ol class="runs">
        {#each runs as r (r.start)}
          <li>
            <div class="run-head">
              <strong class:bad={r.result === 'failed'} class:ok={r.result === 'success'}>
                {r.result === 'success' ? t('jobs.ok') : r.result === 'running' ? t('jobs.state.running') : t('jobs.failedExit', { code: r.exit ?? '?', result: '' })}
              </strong>
              <span class="muted">{when(r.start)}{#if r.end} · {duration(Math.max(0, (r.end - r.start) / 1000)) || '0 s'}{/if}</span>
            </div>
            {#if r.output.length}
              <!-- svelte-ignore a11y_no_noninteractive_tabindex (it scrolls, so the keyboard must reach it) -->
              <pre tabindex="0">{r.output.join('\n')}{r.more ? '\n…' : ''}</pre>
            {/if}
          </li>
        {/each}
      </ol>
    {/if}
  {/if}
</dialog>

<style>
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .note,
  .foot,
  .muted {
    color: var(--text-muted);
  }
  .error,
  .bad {
    color: var(--crit);
  }
  .ok {
    color: var(--ok);
  }
  .warn {
    color: var(--warn);
  }
  .foot {
    font-size: 12px;
    margin-top: var(--space-2);
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
  .scroll {
    overflow-x: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  th,
  td {
    padding: 0 var(--space-3);
    height: var(--row-h);
    text-align: start;
    white-space: nowrap;
    border-bottom: 1px solid var(--border);
  }
  th {
    font-weight: 600;
    color: var(--text-muted);
  }
  tr.paused td {
    color: var(--text-muted);
  }
  .name {
    max-width: 320px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .link {
    font: inherit;
    font-weight: 500;
    color: var(--text);
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .link:hover {
    text-decoration: underline;
  }
  .tag {
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
  }
  .tag.warn {
    color: var(--warn);
    border-color: var(--warn);
  }
  .act {
    width: 1%;
    text-align: end;
  }
  .more-btn {
    font: inherit;
    font-size: 16px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 1px solid transparent;
    border-radius: var(--radius-s);
    padding: 2px 8px;
    cursor: pointer;
  }
  .more-btn:hover,
  .more-btn:focus-visible {
    border-color: var(--border);
  }
  .menu {
    position: fixed;
    inset: auto;
    margin: 0;
    min-width: 170px;
    padding: var(--space-1);
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    font-size: 13px;
  }
  .menu:popover-open {
    display: grid;
  }
  .menu button {
    font: inherit;
    text-align: start;
    color: var(--text);
    background: none;
    border: 0;
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .menu button:hover:not(:disabled),
  .menu button:focus-visible {
    background: var(--surface-2);
  }
  .menu button:disabled {
    color: var(--text-muted);
    cursor: default;
  }
  .menu .danger {
    color: var(--crit);
  }
  .menu hr {
    border: 0;
    border-top: 1px solid var(--border);
    margin: var(--space-1) 0;
    width: 100%;
  }
  .history {
    width: min(720px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    padding: var(--space-4);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  .history::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  .history header {
    align-items: center;
    margin-bottom: var(--space-3);
  }
  .history h2 {
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
  .runs {
    list-style: none;
    margin: 0;
    padding: 0;
    font-size: 13px;
  }
  .runs li {
    padding: var(--space-2) 0;
    border-bottom: 1px solid var(--border);
  }
  .run-head {
    display: flex;
    gap: var(--space-3);
  }
  pre {
    font-family: var(--font-mono, monospace);
    font-size: 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-2);
    margin: var(--space-2) 0 0;
    max-height: 200px;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>

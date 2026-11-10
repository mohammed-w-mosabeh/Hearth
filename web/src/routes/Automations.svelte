<script lang="ts">
  import AutoPause from '../components/AutoPause.svelte';
  import AutomationRuns from '../components/AutomationRuns.svelte';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { preview, reasonKey as actionReason, run, type Plan, type Result } from '../lib/actions';
  import { metricLabel } from '../lib/alerts';
  import { api } from '../lib/api';
  import {
    reasonKey,
    rowKind,
    runStateKey,
    status,
    stepWords,
    triggerWords,
    type Approval,
    type ApprovalList,
    type Automation,
  } from '../lib/automations';
  import { failure } from '../lib/errors';
  import { locale, t, type MessageKey } from '../lib/i18n';
  import { href } from '../lib/router.svelte';
  import { toast } from '../lib/toast.svelte';

  // Automations (ROADMAP 12.9): the list, the global pause, the steps that
  // wait for the user's approval, and each automation's runs. A mode's
  // trigger is changed on the Modes page, an alert rule's trigger on the
  // Alerts page (the server refuses otherwise).

  const REFRESH = 15_000;

  let list = $state.raw<Automation[] | null>(null);
  let approvals = $state.raw<Approval[]>([]);
  let paused = $state(false);
  let connections = $state.raw<{ uuid: string; name: string }[]>([]);
  let failed = $state('');
  let busy = $state<string | null>(null);
  let history = $state<Automation | null>(null);
  let approving = $state<{ approval: Approval; plan: Plan } | null>(null);
  let removing = $state<{ automation: Automation; plan: Plan } | null>(null);

  async function load(): Promise<void> {
    try {
      const [l, ap] = await Promise.all([api<{ automations: Automation[] }>('/automations'), api<ApprovalList>('/automations/approvals')]);
      list = l.automations;
      approvals = ap.automations;
      failed = '';
    } catch (err) {
      failed = failure(err, 'auto.error');
    }
  }

  $effect(() => {
    void load();
    api<{ connections: { uuid: string; name: string }[] }>('/system/network').then((r) => (connections = r.connections ?? []), () => {});
    const timer = setInterval(() => void load(), REFRESH);
    return () => clearInterval(timer);
  });

  async function act(key: string, fn: () => Promise<void>): Promise<void> {
    busy = key;
    try {
      await fn();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = null;
    }
  }

  const toggle = (a: Automation, on: boolean) =>
    act(`row-${a.id}`, async () => {
      await api(`/automations/${a.id}/enabled`, { method: 'PUT', body: JSON.stringify({ enabled: on }) });
      await load();
    });

  const testNow = (a: Automation) =>
    act(`row-${a.id}`, async () => {
      await api(`/automations/${a.id}/test`, { method: 'POST' });
      toast(t('auto.test.started', { s: a.name }));
      // Its record, and an approval if a step waits, come within moments.
      setTimeout(() => void load(), 2000);
    });

  function askRemove(a: Automation): void {
    removing = { automation: a, plan: { summary: '', items: [{ id: String(a.id), name: a.name }], blocked: [], risk: 'destructive' } };
  }

  async function removed(): Promise<void> {
    const c = removing;
    removing = null;
    if (!c) return;
    await act(`row-${c.automation.id}`, async () => {
      await api(`/automations/${c.automation.id}`, { method: 'DELETE' });
      await load();
    });
  }

  function stepText(s: Parameters<typeof stepWords>[0]): string {
    const w = stepWords(s);
    return t(w.key, w.values);
  }

  /** The approval screen: every parked step previewed by its own action;
   *  always confirmed in the dialog (SECURITY S15d). */
  const approve = (a: Approval) =>
    act(`approval-${a.id}`, async () => {
      approving = { approval: a, plan: await preview('automation', 'approve', String(a.id)) };
    });

  function report(name: string, res: Result): void {
    const first = res.failed[0] ?? res.blocked[0];
    if (first) toast(t('auto.failed', { s: name, why: t(actionReason(first.reason), { detail: first.detail ?? '' }) }), { tone: 'error' });
    else toast(t('auto.done', { s: name, n: res.done }));
  }

  async function approveConfirmed(): Promise<void> {
    const c = approving;
    approving = null;
    if (!c) return;
    await act(`approval-${c.approval.id}`, async () => {
      report(c.approval.name, await run('automation', 'approve', String(c.approval.id), {}, c.plan.token));
      await load();
    });
  }

  const dismiss = (a: Approval) =>
    act(`approval-${a.id}`, async () => {
      await run('automation', 'dismiss', String(a.id));
      toast(t('auto.dismissed'));
      await load();
    });

  const when = (ms: number) => new Date(ms).toLocaleString(locale());
  const connectionName = (uuid: string) => connections.find((c) => c.uuid === uuid)?.name ?? uuid;

  function triggerText(a: Automation): string {
    if (!a.spec) return '';
    const w = triggerWords(a.spec.trigger, { connection: connectionName, metric: (m) => metricLabel(undefined, m) });
    return t(w.key, w.values);
  }

  function lastText(a: Automation): string {
    const l = a.last;
    if (!l) return t('auto.never');
    const k = l.reason ? reasonKey(l.reason) : null;
    const why = l.reason ? (k ? t(k) : l.reason) : '';
    return why ? t('auto.lastWhy', { state: t(runStateKey(l.state)), time: when(l.started), why }) : t('auto.last', { state: t(runStateKey(l.state)), time: when(l.started) });
  }

  const STATUS: Record<ReturnType<typeof status>, MessageKey> = {
    on: 'auto.status.on',
    off: 'auto.status.off',
    paused: 'auto.status.paused',
    waiting: 'auto.status.waiting',
    invalid: 'auto.status.invalid',
  };
</script>

<header>
  <div>
    <h1>{t('auto.title')}</h1>
    <p>{t('auto.intro')}</p>
  </div>
</header>

<AutoPause bind:paused />

{#if failed}
  <p class="note error" role="status">{failed}</p>
{:else if list === null}
  <Skeleton label={t('auto.loading')} />
{:else}
  {#if approvals.length > 0}
    <section aria-labelledby="approvals-title">
      <h2 id="approvals-title">{t('auto.approvals')}</h2>
      {#each approvals as a (a.id)}
        <article class="card approval">
          <h3>{t('auto.approval.title', { s: a.name, time: when(a.created) })}</h3>
          <ol>
            {#each a.steps as s, i (i)}<li>{stepText(s)}</li>{/each}
          </ol>
          <p class="note">{t('auto.approval.body', { time: when(a.expires) })}</p>
          <div class="row">
            <button type="button" class="btn primary" onclick={() => approve(a)} disabled={busy === `approval-${a.id}` || paused} aria-busy={busy === `approval-${a.id}`}>{t('auto.approve')}</button>
            <button type="button" class="btn" onclick={() => dismiss(a)} disabled={busy === `approval-${a.id}`}>{t('auto.dismiss')}</button>
          </div>
          {#if paused}<p class="note">{t('auto.approval.paused')}</p>{/if}
        </article>
      {/each}
    </section>
  {/if}

  {#if list.length === 0}
    <p class="note">{t('auto.none')}</p>
  {:else}
    <div class="card table-wrap">
      <table>
        <thead>
          <tr>
            <th scope="col">{t('auto.col.name')}</th>
            <th scope="col">{t('auto.col.when')}</th>
            <th scope="col">{t('auto.col.status')}</th>
            <th scope="col">{t('auto.col.last')}</th>
            <th scope="col"><span class="visually-hidden">{t('auto.col.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each list as a (a.id)}
            {@const kind = rowKind(a)}
            {@const st = status(a, paused)}
            <tr>
              <th scope="row">
                <span class="name">{a.name}</span>
                {#if kind === 'mode'}<span class="badge">{t('auto.origin.mode')}</span>{/if}
                {#if kind === 'alert'}<span class="badge">{t('auto.origin.alert')}</span>{/if}
              </th>
              <td>
                {#if a.spec}
                  <span>{triggerText(a)}</span>
                  {#if a.next}<span class="sub">{t('auto.next', { time: when(a.next) })}</span>{/if}
                {:else}
                  <span class="sub warn">{a.invalid ?? ''}</span>
                {/if}
              </td>
              <td>
                <label class="switch">
                  <input
                    type="checkbox"
                    checked={a.enabled}
                    disabled={kind === 'mode' || kind === 'invalid' || busy === `row-${a.id}`}
                    aria-label={t('auto.toggle', { s: a.name })}
                    onchange={(e) => toggle(a, e.currentTarget.checked)}
                  />
                  <span class="status {st}">{t(STATUS[st])}</span>
                </label>
                {#if (a.waiting ?? 0) > 0}<span class="sub warn">{t('auto.waitingN', { n: a.waiting ?? 0 })}</span>{/if}
              </td>
              <td><span class="sub">{lastText(a)}</span></td>
              <td class="actions">
                <button type="button" class="btn" onclick={() => (history = a)}>{t('auto.history')}</button>
                {#if kind === 'mode'}
                  <a class="btn" href={href('modes')}>{t('auto.openModes')}</a>
                {:else if kind === 'invalid'}
                  <button type="button" class="btn" onclick={() => askRemove(a)} disabled={busy === `row-${a.id}`}>{t('auto.delete')}</button>
                {:else}
                  {#if kind === 'alert'}<a class="btn" href={href('alerts')}>{t('auto.openAlerts')}</a>{/if}
                  <button type="button" class="btn" onclick={() => testNow(a)} disabled={busy === `row-${a.id}` || paused}>{t('auto.test')}</button>
                  <button type="button" class="btn" onclick={() => askRemove(a)} disabled={busy === `row-${a.id}`}>{t('auto.delete')}</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}

<AutomationRuns automation={history} onclose={() => (history = null)} />

<ConfirmDialog
  plan={approving?.plan ?? null}
  title={approving ? t('auto.approve.confirm', { s: approving.approval.name }) : ''}
  verb={t('auto.approve')}
  body={t('auto.approve.body')}
  onconfirm={approveConfirmed}
  oncancel={() => (approving = null)}
/>

<ConfirmDialog
  plan={removing?.plan ?? null}
  title={removing ? t('auto.delete.confirm', { s: removing.automation.name }) : ''}
  verb={t('auto.delete')}
  body={t('auto.delete.body')}
  onconfirm={removed}
  oncancel={() => (removing = null)}
/>

<style>
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--space-4);
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  h2 {
    font-size: 16px;
    margin: 0 0 var(--space-2);
  }
  h3 {
    font-size: 14px;
    margin: 0;
    overflow-wrap: anywhere;
  }
  header p,
  .note {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .error {
    color: var(--crit);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-bottom: var(--space-3);
    min-width: 0;
  }
  .approval {
    border-inline-start: 4px solid var(--warn);
  }
  section {
    margin-bottom: var(--space-4);
  }
  .table-wrap {
    overflow-x: auto;
    padding: 0;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th,
  td {
    text-align: start;
    vertical-align: top;
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--border);
  }
  thead th {
    color: var(--text-muted);
    font-weight: 500;
    font-size: 12px;
  }
  tbody tr:last-child > * {
    border-bottom: 0;
  }
  tbody th {
    font-weight: normal;
    min-width: 12em;
  }
  td {
    overflow-wrap: anywhere;
  }
  .name {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .sub {
    display: block;
    color: var(--text-muted);
    font-size: 12px;
  }
  .sub.warn,
  .status.waiting,
  .status.paused {
    color: var(--warn);
  }
  .status.on {
    color: var(--ok);
  }
  .status.invalid {
    color: var(--crit);
  }
  .badge {
    font-size: 12px;
    padding: 0 var(--space-2);
    margin-inline-start: var(--space-1);
    border-radius: 999px;
    border: 1px solid currentColor;
    white-space: nowrap;
    color: var(--text-muted);
  }
  ol {
    padding-inline-start: var(--space-4);
    margin: var(--space-2) 0 0;
    font-size: 13px;
  }
  li {
    padding: 2px 0;
    overflow-wrap: anywhere;
  }
  .switch {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    font-size: 13px;
  }
  .actions {
    white-space: nowrap;
    text-align: end;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-3);
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
    text-decoration: none;
    display: inline-block;
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

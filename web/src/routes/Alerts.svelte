<script lang="ts">
  import RuleDialog from '../components/RuleDialog.svelte';
  import { applyEvent, blankRule, conditionText, draftOf, lasted, metricLabel, valueText, type AlertEvent, type Metric, type Rule, type RuleDraft } from '../lib/alerts';
  import { api, ApiError } from '../lib/api';
  import { t, type MessageKey } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Alert rules and incidents (ROADMAP 1.14). Rules are evaluated by
  // hearthd whether or not this page is open; it only shows them.
  let { live }: { live: Live } = $props();

  const PAGE = 100;

  let rules = $state.raw<Rule[] | null>(null);
  let metrics = $state.raw<Metric[]>([]);
  let events = $state.raw<AlertEvent[]>([]);
  let more = $state(false);
  let failed = $state(false);
  let draft = $state<RuleDraft | null>(null);
  let editingId = $state<number | null>(null);
  let formError = $state('');

  const metricOf = (id: string) => metrics.find((m) => m.id === id);
  const active = $derived(events.filter((e) => e.ended_at === undefined || e.ended_at === null));
  const sevKey: Record<string, MessageKey> = { info: 'alerts.sev.info', warning: 'alerts.sev.warning', critical: 'alerts.sev.critical' };

  function errorText(err: unknown): string {
    return err instanceof ApiError ? err.message : t('action.failed');
  }

  async function loadRules(): Promise<void> {
    try {
      rules = (await api<{ rules: Rule[] }>('/alerts/rules')).rules;
      failed = false;
    } catch {
      failed = true;
    }
  }

  async function loadEvents(before = 0): Promise<void> {
    try {
      const q = before ? `?limit=${PAGE}&before=${before}` : `?limit=${PAGE}`;
      const got = (await api<{ events: AlertEvent[] }>(`/alerts/events${q}`)).events;
      events = before ? [...events, ...got] : got;
      more = got.length === PAGE;
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void loadRules();
    void loadEvents();
    api<{ metrics: Metric[] }>('/alerts/metrics')
      .then((r) => (metrics = r.metrics))
      .catch(() => {});
    return live.subscribe('alerts', (data) => {
      const m = data as { kind?: string; event?: AlertEvent };
      if (m?.event && typeof m.event.id === 'number') events = applyEvent(events, m.event);
    });
  });

  function add(): void {
    editingId = null;
    formError = '';
    draft = blankRule(metrics[0]);
  }

  function edit(r: Rule): void {
    editingId = r.id;
    formError = '';
    draft = draftOf(r);
  }

  async function save(r: RuleDraft): Promise<void> {
    try {
      if (editingId === null) await api('/alerts/rules', { method: 'POST', body: JSON.stringify(r) });
      else await api(`/alerts/rules/${editingId}`, { method: 'PUT', body: JSON.stringify(r) });
      draft = null;
      await loadRules();
    } catch (err) {
      formError = errorText(err);
    }
  }

  async function toggle(r: Rule): Promise<void> {
    try {
      await api(`/alerts/rules/${r.id}`, { method: 'PUT', body: JSON.stringify({ ...draftOf(r), enabled: !r.enabled }) });
      await loadRules();
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  // Delete with Undo (the rule is recreated with the same settings).
  async function remove(r: Rule): Promise<void> {
    const rest = draftOf(r);
    try {
      await api(`/alerts/rules/${r.id}`, { method: 'DELETE' });
      await loadRules();
      toast(t('alerts.deleted', { name: r.name }), {
        undo: () => {
          void api('/alerts/rules', { method: 'POST', body: JSON.stringify(rest) })
            .then(loadRules)
            .catch((err: unknown) => toast(errorText(err), { tone: 'error' }));
        },
      });
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function test(): Promise<void> {
    try {
      await api('/alerts/test', { method: 'POST' });
      toast(t('alerts.testSent'));
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  const when = (ms: number) => new Date(ms).toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'short' });
  const eventText = (e: AlertEvent) => {
    const m = metricOf(e.metric);
    if (e.metric === 'unit.failed') return t('alerts.event.unit', { unit: e.instance });
    if (e.metric === 'container.exited') return t('alerts.event.container', { name: e.instance });
    const where = e.instance ? ` (${e.instance})` : '';
    return `${metricLabel(m, e.metric)}${where}: ${valueText(e.value, m)}, ${t('alerts.event.peak', { v: valueText(e.peak, m) })}`;
  };
</script>

<header>
  <div>
    <h1>{t('alerts.title')}</h1>
    <p>{t('alerts.intro')}</p>
  </div>
  <div class="actions">
    <button type="button" onclick={test}>{t('alerts.test')}</button>
    <button type="button" class="primary" onclick={add} disabled={metrics.length === 0}>{t('alerts.add')}</button>
  </div>
</header>

{#if failed}<p class="note error" role="status">{t('alerts.error')}</p>{/if}

<section aria-labelledby="active-h">
  <h2 id="active-h">{t('alerts.active')}</h2>
  {#if active.length === 0}
    <p class="note">{t('alerts.noneActive')}</p>
  {:else}
    <ul class="incidents">
      {#each active as e (e.id)}
        <li class={e.severity}>
          <span class="sev">{t(sevKey[e.severity] ?? 'alerts.sev.warning')}</span>
          <strong>{e.rule_name}</strong>
          <span>{eventText(e)}</span>
          <span class="muted">{t('alerts.since', { when: when(e.started_at), d: lasted(e) })}</span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<section aria-labelledby="rules-h">
  <h2 id="rules-h">{t('alerts.rules')}</h2>
  {#if rules === null}
    <p class="note">{t('alerts.loading')}</p>
  {:else if rules.length === 0}
    <p class="note">{t('alerts.noRules')}</p>
  {:else}
    <div class="card">
      <table>
        <tbody>
          {#each rules as r (r.id)}
            <tr class:off={!r.enabled}>
              <td class="sw">
                <input
                  type="checkbox"
                  role="switch"
                  checked={r.enabled}
                  aria-label={t(r.enabled ? 'alerts.disable' : 'alerts.enable', { name: r.name })}
                  onchange={() => toggle(r)}
                />
              </td>
              <td>
                <div class="name">{r.name}</div>
                <div class="muted">{conditionText(r, metricOf(r.metric))}{r.quiet_from ? ` · ${t('alerts.quietShort', { from: r.quiet_from, to: r.quiet_to })}` : ''}</div>
              </td>
              <td><span class="badge {r.severity}">{t(sevKey[r.severity] ?? 'alerts.sev.warning')}</span></td>
              <td class="btns">
                <button type="button" onclick={() => edit(r)} aria-label={t('alerts.editLabel', { name: r.name })}>{t('alerts.edit')}</button>
                <button type="button" onclick={() => remove(r)} aria-label={t('alerts.deleteLabel', { name: r.name })}>{t('alerts.delete')}</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<section aria-labelledby="hist-h">
  <h2 id="hist-h">{t('alerts.history')}</h2>
  {#if events.length === 0}
    <p class="note">{t('alerts.noHistory')}</p>
  {:else}
    <div class="card">
      <table>
        <tbody>
          {#each events as e (e.id)}
            <tr>
              <td class="when">{when(e.started_at)}</td>
              <td><span class="badge {e.severity}">{t(sevKey[e.severity] ?? 'alerts.sev.warning')}</span></td>
              <td>
                <div class="name">{e.rule_name}</div>
                <div class="muted">{eventText(e)}</div>
              </td>
              <td class="muted">
                {e.ended_at ? t('alerts.lastedFor', { d: lasted(e) }) : t('alerts.ongoing')}{e.notified ? '' : ` · ${t('alerts.quietNote')}`}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if more}
      <button type="button" class="more" onclick={() => loadEvents(events.at(-1)?.id ?? 0)}>{t('alerts.more')}</button>
    {/if}
  {/if}
</section>

<RuleDialog
  {draft}
  editing={editingId !== null}
  {metrics}
  error={formError}
  onsave={save}
  oncancel={() => (draft = null)}
/>

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
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: var(--space-5) 0 var(--space-2);
  }
  .actions {
    display: flex;
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
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .note,
  .muted {
    color: var(--text-muted);
  }
  .muted {
    font-size: 12px;
  }
  .error {
    color: var(--crit);
  }
  .incidents {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--space-2);
  }
  .incidents li {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--surface);
    border: 1px solid var(--border);
    border-inline-start: 4px solid var(--warn);
    border-radius: var(--radius-s);
    font-size: 13px;
  }
  .incidents li.critical {
    border-inline-start-color: var(--crit);
  }
  .incidents li.info {
    border-inline-start-color: var(--accent);
  }
  .sev {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted);
  }
  .card {
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
  }
  td {
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  tr:last-child td {
    border-bottom: 0;
  }
  tr.off .name,
  tr.off .muted {
    opacity: 0.55;
  }
  .sw {
    width: 1%;
  }
  .name {
    font-weight: 500;
  }
  .when {
    white-space: nowrap;
    color: var(--text-muted);
    width: 1%;
  }
  .btns {
    width: 1%;
    white-space: nowrap;
    text-align: end;
  }
  .btns button {
    margin-inline-start: var(--space-1);
  }
  .badge {
    font-size: 11px;
    border: 1px solid var(--warn);
    color: var(--warn);
    border-radius: 999px;
    padding: 0 6px;
  }
  .badge.critical {
    border-color: var(--crit);
    color: var(--crit);
  }
  .badge.info {
    border-color: var(--border);
    color: var(--text-muted);
  }
  .more {
    margin-top: var(--space-2);
  }
</style>

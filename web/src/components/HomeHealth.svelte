<script lang="ts">
  import { failure } from '../lib/errors';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import StatusMark from './StatusMark.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { applyEvent, type AlertEvent } from '../lib/alerts';
  import { api } from '../lib/api';
  import type { Forecasts } from '../lib/disks';
  import {
    RESERVED,
    RULES,
    collect,
    queue,
    readSnoozed,
    score,
    severity,
    snooze,
    unchecked,
    type FeedData,
    type Inputs,
    type Item,
  } from '../lib/health';
  import { t, type MessageKey } from '../lib/i18n';
  import type { LynisView } from '../lib/lynis';
  import { href } from '../lib/router.svelte';
  import type { Report } from '../lib/security';
  import { key, type ServicesMsg, type Unit } from '../lib/services';
  import type { Health } from '../lib/smart';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Health ring and Needs attention (ROADMAP 8.6), read from existing
  // endpoints once on load and every 5 minutes; alerts arrive as they fire.
  let { live }: { live: Live } = $props();

  const REFRESH_MS = 5 * 60 * 1000;

  let inputs = $state.raw<Inputs>({});

  function set<F extends keyof FeedData>(feed: F, v: FeedData[F] | null): void {
    inputs = { ...inputs, [feed]: v };
  }

  function load<F extends keyof FeedData>(feed: F, path: string, pick: (v: never) => FeedData[F]): Promise<void> {
    return api<never>(path)
      .then((v) => set(feed, pick(v)))
      .catch(() => set(feed, null));
  }

  const loadUnits = (): Promise<void> => load('units', '/services', (m: ServicesMsg) => (m.kind === 'list' ? m.units : []));

  function refresh(): void {
    void loadUnits();
    void load('alerts', '/alerts/events?limit=500', (r: { events: AlertEvent[] }) => r.events);
    void load('security', '/security', (r: Report) => r);
    void load('forecasts', '/forecasts', (r: Forecasts) => r);
    void load('smart', '/disks/health', (r: Health) => r);
    void load('lynis', '/security/lynis', (r: LynisView) => r);
  }

  $effect(() => {
    refresh();
    const timer = setInterval(refresh, REFRESH_MS);
    const stop = live.subscribe('alerts', (data) => {
      const e = (data as { event?: AlertEvent })?.event;
      if (e && typeof e.id === 'number') set('alerts', applyEvent(inputs.alerts ?? [], e));
    });
    return () => {
      clearInterval(timer);
      stop();
    };
  });

  // Snoozed items: stored per viewer in the home.snoozed setting.
  let snoozed = $state.raw<Record<string, number>>({});
  let now = $state(Date.now());
  $effect(() => {
    api<unknown>('/settings/home.snoozed')
      .then((v) => (snoozed = readSnoozed(v, Date.now())))
      .catch(() => {});
    const tick = setInterval(() => (now = Date.now()), 60_000);
    return () => clearInterval(tick);
  });

  async function snoozeItem(it: Item): Promise<void> {
    const before = snoozed;
    now = Date.now();
    snoozed = snooze(snoozed, it.id, now);
    try {
      await api('/settings/home.snoozed', { method: 'PUT', body: JSON.stringify({ until: snoozed }) });
    } catch {
      snoozed = before;
      toast(t('health.snoozeFailed'), { tone: 'error' });
    }
  }

  const loading = $derived((['units', 'alerts', 'security', 'forecasts', 'smart', 'lynis'] as const).some((f) => inputs[f] === undefined));
  const items = $derived(collect(inputs));
  const result = $derived(score(items));
  const missing = $derived(unchecked(inputs));
  const rows = $derived(queue(items, snoozed, now));
  const hidden = $derived(items.length - rows.length);
  const ringKind = $derived(result.score >= 90 ? 'ok' : result.score >= 60 ? 'warning' : 'failed');
  const verdict = $derived<MessageKey>(result.score >= 90 ? 'health.good' : result.score >= 60 ? 'health.fair' : 'health.poor');

  let open = $state(false);

  // A long queue shows its most serious rows first.
  const SHOW = 8;
  let all = $state(false);
  const shown = $derived(all ? rows : rows.slice(0, SHOW));

  // The ring: r = 52 in a 120 box.
  const C = 2 * Math.PI * 52;

  function itemText(it: Item): string {
    if (it.source.startsWith('alert.')) return t('health.item.alert', { subject: it.subject });
    if (it.source === 'restart') return t(it.subject === 'reboot' ? 'health.item.reboot' : 'health.item.services');
    return t(`health.item.${it.source}` as MessageKey, { subject: it.subject });
  }

  function actionLabel(it: Item): string {
    return it.action.kind === 'unit' ? t('svc.op.restart') : t(`health.open.${it.action.route}` as MessageKey);
  }

  // Restarting a failed unit: the Services page's flow, preview first and
  // the confirmation the server asks for on a critical unit.
  let pending = $state<{ unit: Unit; plan: Plan } | null>(null);
  let busy = $state.raw<ReadonlySet<string>>(new Set());

  function errorText(err: unknown): string {
    return failure(err, 'action.failed');
  }

  function report(u: Unit, res: Result): void {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done === 0 && bad) {
      const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
      toast(t('svc.failed', { op: t('svc.op.restart'), name: u.name, reason }), { tone: 'error' });
      return;
    }
    toast(t('svc.done.restart', { name: u.name }));
  }

  async function execute(u: Unit, token?: string, typed?: string): Promise<void> {
    const k = key(u);
    busy = new Set([...busy, k]);
    if (u.scope === 'system') toast(t('svc.authHint'));
    try {
      report(u, await run('unit', 'restart', k, {}, token, typed));
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      busy = new Set([...busy].filter((x) => x !== k));
      void loadUnits();
    }
  }

  async function restart(u: Unit): Promise<void> {
    try {
      const plan = await preview('unit', 'restart', key(u));
      if (plan.items.length === 0) report(u, { done: 0, failed: [], blocked: plan.blocked });
      else if (plan.risk === 'destructive') pending = { unit: u, plan };
      else await execute(u);
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { unit: u, plan } = pending;
    pending = null;
    await execute(u, plan.token, typed);
  }
</script>

<section class="health" aria-labelledby="health-title">
  <div class="summary">
    <h2 id="health-title" class="visually-hidden">{t('health.title')}</h2>
    {#if loading}
      <div class="ring placeholder" aria-hidden="true"></div>
      <p class="muted" role="status">{t('health.checking')}</p>
    {:else}
      <button type="button" class="ring" aria-expanded={open} aria-controls="health-deductions" onclick={() => (open = !open)}>
        <svg viewBox="0 0 120 120" aria-hidden="true">
          <circle class="track" cx="60" cy="60" r="52" />
          <circle class="arc {ringKind}" cx="60" cy="60" r="52" stroke-dasharray="{(C * result.score) / 100} {C}" />
        </svg>
        <span class="num" aria-hidden="true">{result.score}</span>
        <span class="visually-hidden">{t('health.ring', { score: result.score })}</span>
      </button>
      <div>
        <p class="verdict"><StatusMark kind={ringKind} label={t(verdict)} /> {t(verdict)}</p>
        <p class="muted">{open ? t('health.hide') : t('health.show')}</p>
      </div>
    {/if}
  </div>

  {#if open && !loading}
    <div id="health-deductions" class="deductions">
      {#if result.deductions.length === 0}
        <p class="muted">{t('health.none')}</p>
      {:else}
        <table>
          <thead>
            <tr><th scope="col">{t('health.col.source')}</th><th scope="col">{t('health.col.count')}</th><th scope="col">{t('health.col.points')}</th></tr>
          </thead>
          <tbody>
            {#each result.deductions as d (d.source)}
              <tr>
                <td>{t(`health.src.${d.source}` as MessageKey)}</td>
                <td class="n">{d.count}</td>
                <td class="n">−{d.points}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
      <p class="muted">{t('health.rules')}</p>
      <ul class="rules">
        {#each Object.entries(RULES) as [source, r] (source)}
          <li>{t(`health.src.${source}` as MessageKey)}: {t('health.rule', { each: r.each, cap: r.cap })}</li>
        {/each}
      </ul>
      {#if missing.length > 0}
        <p class="warn">{t('health.unchecked', { list: missing.map((f) => t(`health.feed.${f}` as MessageKey)).join(', ') })}</p>
      {/if}
      <p class="muted">{t('health.reserved', { n: RESERVED.length })}</p>
    </div>
  {/if}

  <div class="attention">
    <h2>{t('health.attention')}</h2>
    {#if loading && items.length === 0}
      <p class="muted">{t('health.checking')}</p>
    {:else if rows.length === 0}
      <p class="muted">{t('health.attentionNone')}</p>
    {:else}
      <ul>
        {#each shown as it (it.id)}
          {@const text = itemText(it)}
          <li>
            <StatusMark kind={severity(it.source)} label={t(`health.sev.${severity(it.source)}` as MessageKey)} />
            <span class="text" title={text}>{text}</span>
            {#if it.action.kind === 'unit'}
              {@const u = it.action.unit}
              <button type="button" disabled={busy.has(key(u))} onclick={() => restart(u)}>{actionLabel(it)}</button>
            {:else}
              <a class="button" href={href(it.action.route)}>{actionLabel(it)}</a>
            {/if}
            <button type="button" class="quiet" aria-label={t('health.snoozeLabel', { name: text })} onclick={() => snoozeItem(it)}>{t('health.snooze')}</button>
          </li>
        {/each}
      </ul>
    {/if}
    {#if rows.length > SHOW}
      <button type="button" class="quiet more" aria-expanded={all} onclick={() => (all = !all)}>
        {all ? t('health.fewer') : t('health.more', { n: rows.length - SHOW })}
      </button>
    {/if}
    {#if hidden > 0}<p class="muted">{t('health.snoozedCount', { n: hidden })}</p>{/if}
  </div>
</section>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('svc.confirmTitle', { op: t('svc.op.restart'), name: pending.unit.name }) : ''}
  verb={t('svc.op.restart')}
  warning={t('svc.criticalWarning')}
  body={t('svc.confirmBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<style>
  .health {
    display: grid;
    grid-template-columns: minmax(220px, 300px) 1fr;
    gap: var(--space-4);
    align-items: start;
    margin-bottom: var(--space-5);
  }
  .summary,
  .attention,
  .deductions {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-4);
    min-width: 0;
  }
  .summary {
    display: flex;
    align-items: center;
    gap: var(--space-4);
  }
  .deductions {
    grid-column: 1 / -1;
    grid-row: 2;
  }
  .ring {
    position: relative;
    flex: none;
    width: 112px;
    height: 112px;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: none;
    color: var(--text);
    cursor: pointer;
  }
  .ring.placeholder {
    border: 10px solid var(--surface-2);
  }
  svg {
    width: 100%;
    height: 100%;
    transform: rotate(-90deg);
  }
  :global([dir='rtl']) svg {
    transform: rotate(-90deg) scaleY(-1);
  }
  circle {
    fill: none;
    stroke-width: 10;
  }
  .track {
    stroke: var(--surface-2);
  }
  .arc {
    stroke-linecap: round;
    transition: stroke-dasharray var(--motion);
  }
  .arc.ok {
    stroke: var(--ok);
  }
  .arc.warning {
    stroke: var(--warn);
  }
  .arc.failed {
    stroke: var(--crit);
  }
  .num {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    font-family: var(--font-mono);
    font-size: 28px;
    font-variant-numeric: tabular-nums;
  }
  .verdict {
    margin: 0;
    font-weight: 600;
  }
  .muted {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .warn {
    color: var(--warn);
  }
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin-bottom: var(--space-2);
  }
  table {
    border-collapse: collapse;
    margin-bottom: var(--space-2);
  }
  th,
  td {
    text-align: start;
    padding-block: var(--space-1);
    padding-inline: 0 var(--space-4);
  }
  th {
    color: var(--text-muted);
    font-weight: 400;
    font-size: 12px;
  }
  .n {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    text-align: end;
  }
  .rules {
    margin: var(--space-1) 0 0;
    padding-inline-start: var(--space-4);
    color: var(--text-muted);
    font-size: 12px;
  }
  .attention ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .attention li {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) 0;
    border-block-end: 1px solid var(--border);
  }
  .attention li:last-child {
    border-block-end: 0;
  }
  .text {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  button,
  .button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    text-decoration: none;
    white-space: nowrap;
  }
  button:hover:not(:disabled),
  .button:hover {
    background: var(--surface-2);
  }
  button:disabled {
    opacity: 0.4;
    cursor: default;
  }
  button.more {
    margin-top: var(--space-2);
    padding-inline: 0;
  }
  button.quiet {
    border-color: transparent;
    color: var(--text-muted);
  }
  @media (max-width: 720px) {
    .health {
      grid-template-columns: 1fr;
    }
    .deductions {
      grid-row: auto;
    }
  }
</style>

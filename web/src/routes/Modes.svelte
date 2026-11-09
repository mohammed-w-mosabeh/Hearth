<script lang="ts">
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import ModeDialog from '../components/ModeDialog.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { api } from '../lib/api';
  import { failure } from '../lib/errors';
  import { locale, t } from '../lib/i18n';
  import {
    TEMPLATES,
    doKey,
    draftFrom,
    isDo,
    isSetting,
    kindKey,
    sessionCount,
    settingKey,
    templateKey,
    triggerDetail,
    triggerKey,
    type ActiveMode,
    type Approval,
    type Connection,
    type Draft,
    type Item,
    type Mode,
    type ModeList,
    type Profile,
    type Trigger,
  } from '../lib/modes';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  let { live }: { live: Live } = $props();

  // A trigger or a game can switch a mode while this page is open.
  const REFRESH = 15_000;

  let list = $state.raw<ModeList | null>(null);
  let profiles = $state.raw<Profile[]>([]);
  let connections = $state.raw<Connection[]>([]);
  let failed = $state('');
  let busy = $state<string | null>(null);
  let draft = $state<Draft | null>(null);
  let switching = $state<{ name: string; number: number; plan: Plan } | null>(null);
  let reverting = $state<{ name: string; number: number; plan: Plan } | null>(null);
  let removing = $state<{ name: string; number: number; plan: Plan } | null>(null);
  let approving = $state<{ approval: Approval; plan: Plan } | null>(null);

  /** A mode the list shows: one with a valid file, or one still on whose file went. */
  interface Row {
    key: string;
    name: string;
    number: number;
    mode?: Mode;
    active?: ActiveMode | undefined;
  }

  const rows = $derived.by<Row[]>(() => {
    const l = list;
    if (!l) return [];
    const out: Row[] = l.modes.map((m) => ({ key: m.id, name: m.name, number: m.number, mode: m, active: l.active.find((a) => a.id === m.id) }));
    for (const a of l.active) if (!l.modes.some((m) => m.id === a.id)) out.push({ key: a.id, name: a.id, number: a.number, active: a });
    return out;
  });

  async function load(): Promise<void> {
    try {
      const [l, p] = await Promise.all([api<ModeList>('/modes'), api<{ profiles: Profile[] }>('/profiles')]);
      list = l;
      profiles = p.profiles;
      failed = '';
    } catch (err) {
      failed = failure(err, 'modes.error');
    }
  }

  $effect(() => {
    void load();
    api<{ connections: Connection[] }>('/system/network').then((r) => (connections = r.connections ?? []), () => {});
    const timer = setInterval(() => void load(), REFRESH);
    return () => clearInterval(timer);
  });

  function report(name: string, res: Result): void {
    const first = res.failed[0];
    if (first) {
      toast(t('modes.failed', { s: name, n: res.failed.length, why: t(reasonKey(first.reason), { detail: first.detail ?? '' }) }), { tone: 'error' });
    } else {
      toast(t('modes.done', { s: name, n: res.done }));
    }
  }

  const toastFailure = (err: unknown): void => void toast(failure(err, 'action.failed'), { tone: 'error' });

  /** An item in words: a setting by its name, a connection by its name, start or stop translated. */
  function words(it: { name: string; detail?: string }): { name: string; detail?: string } {
    const name = isSetting(it.name) ? t(settingKey(it.name)) : (connections.find((c) => c.uuid === it.name)?.name ?? it.name);
    const d = it.detail ?? '';
    return { ...it, name, detail: isDo(d) ? t(doKey(d)) : d };
  }

  const readable = (plan: Plan): Plan => ({
    ...plan,
    items: plan.items.map((it) => ({ ...it, ...words(it) })),
    blocked: plan.blocked.map((b) => ({ ...b, ...words(b) })),
  });

  async function act(key: string, fn: () => Promise<void>): Promise<void> {
    busy = key;
    try {
      await fn();
    } catch (err) {
      toastFailure(err);
    } finally {
      busy = null;
    }
  }

  /** Shows what will really change; starting things alone needs no dialog. */
  const switchOn = (r: Row) =>
    act(r.key, async () => {
      const plan = await preview('profile', 'activate', String(r.number));
      if (plan.token || plan.blocked.length > 0) {
        switching = { name: r.name, number: r.number, plan: readable(plan) };
        return;
      }
      report(r.name, await run('profile', 'activate', String(r.number)));
      await load();
    });

  async function switchConfirmed(): Promise<void> {
    const c = switching;
    switching = null;
    if (!c) return;
    await act(String(c.number), async () => {
      report(c.name, await run('profile', 'activate', String(c.number), {}, c.plan.token));
      await load();
    });
  }

  /** The revert view: what goes back, and what stays because it was changed by hand. */
  const revert = (r: Row) =>
    act(r.key, async () => {
      reverting = { name: r.name, number: r.number, plan: readable(await preview('profile', 'deactivate', String(r.number))) };
    });

  async function revertConfirmed(): Promise<void> {
    const c = reverting;
    reverting = null;
    if (!c) return;
    await act(String(c.number), async () => {
      report(c.name, await run('profile', 'deactivate', String(c.number), {}, c.plan.token));
      await load();
    });
  }

  function edit(r: Row): void {
    const p = profiles.find((x) => x.id === r.number);
    if (p) draft = { id: p.id, name: p.name, items: p.items, auto_game: p.auto_game };
  }

  function askRemove(r: Row): void {
    removing = { name: r.name, number: r.number, plan: { summary: '', items: [{ id: String(r.number), name: r.name }], blocked: [], risk: 'destructive' } };
  }

  async function removed(): Promise<void> {
    const c = removing;
    removing = null;
    if (!c) return;
    await act(String(c.number), async () => {
      await api(`/profiles/${c.number}`, { method: 'DELETE' });
      await load();
    });
  }

  const triggerHead = (tr: Trigger): string => (tr.kind === 'battery' ? t('modes.trigger.battery', { n: tr.below ?? '' }) : t(triggerKey(tr)));

  function triggerText(tr: Trigger): string {
    const detail = triggerDetail(tr, connections);
    return detail ? `${triggerHead(tr)}: ${detail}` : triggerHead(tr);
  }

  /** Arming is its own click, for the file as shown (SECURITY S15b). */
  const arm = (m: Mode, i: number, tr: Trigger, on: boolean) =>
    act(`${m.id}#${i}`, async () => {
      const res = await run('mode', on ? 'arm' : 'disarm', m.id, { trigger: String(i), hash: m.hash });
      if (res.failed.length > 0) report(m.name, res);
      else toast(t(on ? 'modes.armed.on' : 'modes.armed.off', { s: `${m.name}: ${triggerText(tr)}` }));
      await load();
    });

  const approve = (a: Approval) =>
    act(`approval-${a.id}`, async () => {
      const plan = await preview('mode', 'approve', String(a.id));
      if (plan.token) {
        approving = { approval: a, plan: readable(plan) };
        return;
      }
      report(a.name, await run('mode', 'approve', String(a.id)));
      await load();
    });

  async function approveConfirmed(): Promise<void> {
    const c = approving;
    approving = null;
    if (!c) return;
    await act(`approval-${c.approval.id}`, async () => {
      report(c.approval.name, await run('mode', 'approve', String(c.approval.id), {}, c.plan.token));
      await load();
    });
  }

  const dismiss = (a: Approval) =>
    act(`approval-${a.id}`, async () => {
      await run('mode', 'dismiss', String(a.id));
      toast(t('modes.dismissed'));
      await load();
    });

  const when = (iso: string) => new Date(iso).toLocaleString(locale());
  const REASONS = ['schedule', 'wifi', 'battery', 'display', 'game'] as const;
  const reasonText = (reason: string): string =>
    (REASONS as readonly string[]).includes(reason) ? t(`modes.reason.${reason as (typeof REASONS)[number]}`) : reason;
  const sinceText = (a: ActiveMode): string =>
    t(a.by === 'game' ? 'modes.since.game' : a.by === 'user' ? 'modes.since.user' : 'modes.since.trigger', { time: when(a.since) });
  const stackCount = (m: Mode): { n: number; m: number } => ({ n: m.stacks.length, m: m.items.length + (m.session.open?.length ?? 0) });
  const itemText = (it: Item): string => (isDo(it.do) ? t(doKey(it.do, it.kind)) : it.do);
</script>

<header>
  <div>
    <h1>{t('modes.title')}</h1>
    <p>{t('modes.intro')}</p>
  </div>
  <button type="button" class="btn primary" onclick={() => (draft = { name: '', items: [], auto_game: false })}>{t('modes.new')}</button>
</header>

{#if failed}
  <p class="note error" role="status">{failed}</p>
{:else if list === null}
  <Skeleton label={t('modes.loading')} />
{:else}
  {#if list.approvals.length > 0}
    <section aria-labelledby="approvals-title">
      <h2 id="approvals-title">{t('modes.approvals')}</h2>
      {#each list.approvals as a (a.id)}
        <article class="card approval">
          <h3>{t('modes.approval.title', { s: a.name, time: when(a.created_at), why: reasonText(a.reason) })}</h3>
          <ul>
            {#each a.items as it (it.kind + it.target)}
              <li><span class="target">{it.target}</span> <span class="kind">{t(kindKey(it.kind))}</span> <span class="do {it.do}">{itemText(it)}</span></li>
            {/each}
          </ul>
          <p class="note">{t('modes.approval.body')}</p>
          <div class="row">
            <button type="button" class="btn primary" onclick={() => approve(a)} disabled={busy === `approval-${a.id}`} aria-busy={busy === `approval-${a.id}`}>{t('modes.approve')}</button>
            <button type="button" class="btn" onclick={() => dismiss(a)} disabled={busy === `approval-${a.id}`}>{t('modes.dismiss')}</button>
          </div>
        </article>
      {/each}
    </section>
  {/if}

  {#if rows.length === 0}
    <p class="note">{t('modes.none')}</p>
    <div class="row">
      {#each TEMPLATES as tpl (tpl)}
        <button type="button" class="btn" onclick={() => (draft = draftFrom(tpl, t(templateKey(tpl))))}>{t(templateKey(tpl))}</button>
      {/each}
    </div>
  {:else}
    <div class="card table-wrap">
      <table>
        <thead>
          <tr>
            <th scope="col">{t('modes.col.mode')}</th>
            <th scope="col">{t('modes.col.session')}</th>
            <th scope="col">{t('modes.col.stacks')}</th>
            <th scope="col">{t('modes.col.triggers')}</th>
            <th scope="col"><span class="visually-hidden">{t('modes.col.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as r (r.key)}
            <tr class:on={r.active}>
              <th scope="row">
                <span class="name">{r.name}</span>
                {#if r.active}<span class="badge on">● {t('modes.active')}</span>{/if}
                {#if r.active}<span class="sub">{sinceText(r.active)}</span>{/if}
                {#if r.active?.changed}<span class="sub warn">{r.mode ? t('modes.fileChanged') : t('modes.fileGone')}</span>{/if}
              </th>
              {#if r.mode}
                {@const m = r.mode}
                {@const c = stackCount(m)}
                <td>{t('modes.count.session', { n: sessionCount(m.session) })}</td>
                <td>{t('modes.count.stacks', { n: c.n, m: c.m })}</td>
                <td>
                  {#if m.triggers.length === 0}
                    <span class="sub">{t('modes.noTrigger')}</span>
                  {:else}
                    <ul class="triggers">
                      {#each m.triggers as tr, i (i)}
                        <li>
                          <label>
                            <input
                              type="checkbox"
                              checked={m.armed.includes(i)}
                              disabled={busy === `${m.id}#${i}`}
                              aria-label={t('modes.armLabel', { s: triggerText(tr) })}
                              onchange={(e) => arm(m, i, tr, e.currentTarget.checked)}
                            />
                            <span>{triggerHead(tr)}{#if triggerDetail(tr, connections)}: <bdi dir="ltr" class="trig">{triggerDetail(tr, connections)}</bdi>{/if}</span>
                          </label>
                        </li>
                      {/each}
                    </ul>
                  {/if}
                </td>
              {:else}
                <td></td><td></td><td></td>
              {/if}
              <td class="actions">
                {#if r.active}
                  <button type="button" class="btn" onclick={() => revert(r)} disabled={busy === r.key} aria-busy={busy === r.key}>{t('modes.revert')}</button>
                {:else}
                  <button type="button" class="btn" onclick={() => switchOn(r)} disabled={busy === r.key} aria-busy={busy === r.key}>{t('modes.switch')}</button>
                {/if}
                {#if r.mode}
                  <button type="button" class="btn" onclick={() => edit(r)} disabled={!profiles.some((p) => p.id === r.number)}>{t('modes.edit')}</button>
                  <button type="button" class="btn" onclick={() => askRemove(r)} disabled={r.active !== undefined} title={r.active ? t('modes.deleteActive') : undefined}>{t('modes.delete')}</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="note">{t('modes.armHint')}</p>
  {/if}

  {#if list.errors.length > 0}
    <section class="card" aria-labelledby="mode-errors">
      <p id="mode-errors" class="note">{t('modes.fileErrors')}</p>
      <ul>
        {#each list.errors as e (e.file)}
          <li><span class="target">{e.file}</span> <span class="kind">{e.error}</span></li>
        {/each}
      </ul>
    </section>
  {/if}
{/if}

<ModeDialog
  {draft}
  {live}
  onsaved={() => {
    draft = null;
    void load();
  }}
  oncancel={() => (draft = null)}
/>

<ConfirmDialog
  plan={switching?.plan ?? null}
  title={switching ? t('modes.confirm', { s: switching.name }) : ''}
  verb={t('modes.switch')}
  body={t('modes.confirmBody')}
  onconfirm={switchConfirmed}
  oncancel={() => (switching = null)}
/>

<ConfirmDialog
  plan={reverting?.plan ?? null}
  title={reverting ? t('modes.revertTitle', { s: reverting.name }) : ''}
  verb={t('modes.revert')}
  body={reverting && reverting.plan.items.length === 0 ? t('modes.revertNothing') : t('modes.revertBody')}
  onconfirm={revertConfirmed}
  oncancel={() => (reverting = null)}
/>

<ConfirmDialog
  plan={approving?.plan ?? null}
  title={approving ? t('modes.approve.confirm', { s: approving.approval.name }) : ''}
  verb={t('modes.approve')}
  body={t('modes.approve.body')}
  onconfirm={approveConfirmed}
  oncancel={() => (approving = null)}
/>

<ConfirmDialog
  plan={removing?.plan ?? null}
  title={removing ? t('modes.confirmDelete', { s: removing.name }) : ''}
  verb={t('modes.delete')}
  body={t('modes.confirmDeleteBody')}
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
  tr.on > th {
    box-shadow: inset 4px 0 0 var(--ok);
  }
  :global([dir='rtl']) tr.on > th {
    box-shadow: inset -4px 0 0 var(--ok);
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
  .sub.warn {
    color: var(--warn);
  }
  .target,
  .trig {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .badge {
    font-size: 12px;
    padding: 0 var(--space-2);
    margin-inline-start: var(--space-1);
    border-radius: 999px;
    border: 1px solid currentColor;
    white-space: nowrap;
  }
  .badge.on {
    color: var(--ok);
  }
  ul {
    list-style: none;
    padding: 0;
    margin: var(--space-2) 0 0;
  }
  li {
    padding: 2px 0;
    font-size: 13px;
  }
  .triggers {
    margin: 0;
  }
  .triggers label {
    display: flex;
    gap: var(--space-2);
    align-items: baseline;
  }
  .kind {
    color: var(--text-muted);
    font-size: 12px;
  }
  .do {
    color: var(--ok);
    font-size: 12px;
  }
  .do.stop {
    color: var(--warn);
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

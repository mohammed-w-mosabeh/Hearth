<script lang="ts">
  // Uninstall completely (ROADMAP 11.9): with ?item=, the full preview of
  // one item, its run and its result; without, Recently removed, where an
  // uninstall of the last 7 days can be restored.
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { preview, run, type Plan, type Result } from '../lib/actions';
  import { api } from '../lib/api';
  import { failure } from '../lib/errors';
  import { bytes } from '../lib/format';
  import { locale, t, type MessageKey } from '../lib/i18n';
  import { href, router } from '../lib/router.svelte';
  import { toast } from '../lib/toast.svelte';
  import {
    DESKTOP_WORDS,
    defaults,
    fixed,
    freed,
    grouped,
    keyOf,
    needed,
    params,
    selectable,
    typedOk,
    type Candidate,
    type Group,
    type Operation,
    type UninstallPlan,
  } from '../lib/uninstall';

  const groupLabel: Record<Group, MessageKey> = {
    program: 'uninstall.group.program',
    orphans: 'uninstall.group.orphans',
    purge: 'uninstall.group.purge',
    running: 'uninstall.group.running',
    user: 'uninstall.group.user',
    system: 'uninstall.group.system',
    refs: 'uninstall.group.refs',
    remains: 'uninstall.group.remains',
  };
  const stateLabel: Record<Operation['state'], MessageKey> = {
    running: 'uninstall.state.running',
    done: 'uninstall.state.done',
    failed: 'uninstall.state.failed',
    restored: 'uninstall.state.restored',
  };

  let plan = $state.raw<UninstallPlan | null>(null);
  let failed = $state('');
  let loading = $state(false);
  let sel = $state.raw<Set<string>>(new Set());
  let desktop = $state('');
  let data = $state<Record<string, string>>({});
  let confirmPlan = $state.raw<Plan | null>(null);
  let pendingParams: Record<string, string> = {};
  let running = $state(false);
  let result = $state.raw<Result | null>(null);
  let ops = $state.raw<Operation[] | null>(null);
  let restoring = $state.raw<{ op: Operation; plan: Plan } | null>(null);

  async function loadPlan(item: string): Promise<void> {
    loading = true;
    plan = null;
    result = null;
    failed = '';
    try {
      const p = await api<UninstallPlan>(`/uninstall/plan?item=${encodeURIComponent(item)}`);
      plan = p;
      sel = defaults(p);
      desktop = '';
      data = {};
    } catch (err) {
      failed = failure(err, 'uninstall.error');
    } finally {
      loading = false;
    }
  }

  async function loadOps(): Promise<void> {
    try {
      ops = await api<Operation[]>('/uninstall/ops');
    } catch (err) {
      failed = failure(err, 'uninstall.historyError');
    }
  }

  $effect(() => {
    const item = router.item;
    if (item) void loadPlan(item);
    else {
      plan = null;
      result = null;
      void loadOps();
    }
  });

  function toggle(c: Candidate): void {
    // eslint-disable-next-line svelte/prefer-svelte-reactivity -- a new set replaces the $state.raw value whole
    const next = new Set(sel);
    const k = keyOf(c);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    sel = next;
  }

  const total = $derived(plan ? freed(plan, sel) : 0);
  const need = $derived(plan ? needed(plan, sel) : { desktop: false, data: [] });
  const ready = $derived(!!plan && !plan.blocked && typedOk(plan, sel, desktop, data) && !running);

  async function ask(): Promise<void> {
    if (!plan) return;
    pendingParams = params(plan, sel, desktop, data);
    try {
      confirmPlan = await preview('uninstall', 'remove', plan.item, pendingParams);
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    const c = confirmPlan;
    confirmPlan = null;
    if (!c || !plan) return;
    running = true;
    try {
      result = await run('uninstall', 'remove', plan.item, pendingParams, c.token, typed);
      toast(result.failed.length ? t('uninstall.stopped') : t('uninstall.done', { name: plan.name }), { tone: result.failed.length ? 'error' : 'info' });
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      running = false;
    }
  }

  async function askRestore(op: Operation): Promise<void> {
    try {
      restoring = { op, plan: await preview('uninstall', 'restore', op.id) };
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function restored(): Promise<void> {
    const r = restoring;
    restoring = null;
    if (!r) return;
    try {
      const res = await run('uninstall', 'restore', r.op.id, {}, r.plan.token);
      toast(res.failed.length ? t('uninstall.restorePartly', { n: res.failed.length }) : t('uninstall.restoredDone', { name: r.op.name }), {
        tone: res.failed.length ? 'error' : 'info',
      });
      await loadOps();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  const when = (ms: number) => new Date(ms).toLocaleString(locale());
  const doneSteps = (op: Operation) => op.steps.filter((s) => s.done).length;
</script>

{#if router.item}
  <header>
    <div>
      <h1>{plan ? t('uninstall.title', { name: plan.name }) : t('uninstall.titleLoading')}</h1>
      <p>{t('uninstall.intro')}</p>
    </div>
    <a class="btn" href={href('removed')}>{t('uninstall.history')}</a>
  </header>

  {#if failed}
    <p class="note error" role="status">{failed}</p>
  {:else if loading || plan === null}
    <Skeleton label={t('uninstall.loading')} />
  {:else if result}
    <section class="result" aria-live="polite">
      <h2>{result.failed.length ? t('uninstall.result.stopped') : t('uninstall.result.done')}</h2>
      <p>{t('uninstall.result.counts', { done: result.done, failed: result.failed.length, size: bytes(total) })}</p>
      {#if result.failed.length}
        <ul class="failed">
          {#each result.failed as f (f.id + f.reason)}
            <li><strong>{f.name}</strong> <span class="note">{f.detail ?? ''}</span></li>
          {/each}
        </ul>
        <p class="note">{t('uninstall.result.notStarted')}</p>
      {/if}
      {#if plan.candidates.some((c) => c.group === 'remains')}
        <h3>{t('uninstall.group.remains')}</h3>
        <ul>
          {#each plan.candidates.filter((c) => c.group === 'remains') as c (keyOf(c))}
            <li><span class="path">{c.name}</span> <span class="note">{c.reason}</span></li>
          {/each}
        </ul>
      {/if}
      <a class="btn" href={href('removed')}>{t('uninstall.history')}</a>
    </section>
  {:else}
    {@const p = plan}
    {#if p.blocked}<p class="banner crit" role="alert">{t('uninstall.blocked', { why: p.blocked })}</p>{/if}
    {#if need.desktop}
      <div class="banner crit" role="alert">
        <p>{t('uninstall.desktopWarning', { pkgs: (p.desktop ?? []).join(', ') })}</p>
        <label>{t('uninstall.typeWords', { words: DESKTOP_WORDS })}
          <input type="text" bind:value={desktop} autocomplete="off" spellcheck="false" />
        </label>
      </div>
    {/if}

    {#each grouped(p) as [g, rows] (g)}
      <section class="group" aria-labelledby={`g-${g}`}>
        <h2 id={`g-${g}`}>{t(groupLabel[g])} <span class="count">{rows.length}</span></h2>
        <div class="table">
          <table>
            <tbody>
              {#each rows as c (keyOf(c))}
                <tr class:data={c.warn === 'data'}>
                  <td class="pick">
                    {#if selectable(c)}
                      <input type="checkbox" aria-label={c.name} checked={sel.has(keyOf(c))} disabled={fixed(c) || running} onchange={() => toggle(c)} />
                    {/if}
                  </td>
                  <td class="wrap">
                    <span class="path">{c.name}</span>
                    <span class="note">{c.reason}</span>
                    {#if c.warn === 'data'}
                      <span class="tag crit">{t('uninstall.containsData')}</span>
                      {#if sel.has(keyOf(c))}
                        <label class="typed">{t('uninstall.typePath')}
                          <input type="text" bind:value={data[c.id]} autocomplete="off" spellcheck="false" />
                        </label>
                      {/if}
                    {:else if c.warn === 'large'}
                      <span class="tag">{t('uninstall.large')}</span>
                    {:else if c.warn === 'blocks'}
                      <span class="tag crit">{t('uninstall.blocks')}</span>
                    {/if}
                  </td>
                  <td class="n">{c.size ? bytes(c.size) : ''}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/each}

    <div class="footer">
      <strong>{t('uninstall.freed', { size: bytes(total) })}</strong>
      {#if need.data.length}<span class="note">{t('uninstall.typeEach', { n: need.data.length })}</span>{/if}
      <button type="button" class="btn danger" disabled={!ready} aria-busy={running} onclick={ask}>{t('uninstall.action')}</button>
    </div>
  {/if}
{:else}
  <header>
    <div>
      <h1>{t('uninstall.historyTitle')}</h1>
      <p>{t('uninstall.historyIntro')}</p>
    </div>
  </header>
  {#if failed}
    <p class="note error" role="status">{failed}</p>
  {:else if ops === null}
    <Skeleton label={t('uninstall.loading')} />
  {:else if ops.length === 0}
    <p class="note">{t('uninstall.historyNone')}</p>
  {:else}
    <div class="table">
      <table>
        <thead>
          <tr><th>{t('uninstall.col.item')}</th><th>{t('uninstall.col.when')}</th><th>{t('uninstall.col.state')}</th><th>{t('uninstall.col.freed')}</th><th></th></tr>
        </thead>
        <tbody>
          {#each ops as op (op.id)}
            <tr>
              <td class="wrap"><span class="path">{op.name}</span> <span class="note">{op.item}</span></td>
              <td class="n">{when(op.created)}</td>
              <td>{t(stateLabel[op.state])}{op.failed ? ` (${op.failed})` : ''} <span class="note">{t('uninstall.steps', { done: doneSteps(op), n: op.steps.length })}</span></td>
              <td class="n">{bytes(op.freed)}</td>
              <td class="n">
                {#if op.state === 'done' || op.state === 'failed'}
                  <button type="button" class="btn" onclick={() => askRestore(op)}>{t('uninstall.restore')}</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}

<ConfirmDialog
  plan={confirmPlan}
  title={plan ? t('uninstall.confirmTitle', { name: plan.name }) : ''}
  verb={t('uninstall.action')}
  body={t('uninstall.confirmBody')}
  warning={need.desktop ? t('uninstall.desktopWarning', { pkgs: (plan?.desktop ?? []).join(', ') }) : ''}
  onconfirm={confirmed}
  oncancel={() => (confirmPlan = null)}
/>
<ConfirmDialog
  plan={restoring?.plan ?? null}
  title={restoring ? t('uninstall.restoreTitle', { name: restoring.op.name }) : ''}
  verb={t('uninstall.restore')}
  body={t('uninstall.restoreBody')}
  onconfirm={restored}
  oncancel={() => (restoring = null)}
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
    font-size: 15px;
    margin: 0 0 var(--space-2);
  }
  h3 {
    font-size: 14px;
  }
  header p,
  .note {
    color: var(--text-muted);
    font-size: 13px;
  }
  header p {
    font-size: inherit;
    margin: var(--space-1) 0 0;
  }
  .error {
    color: var(--crit);
  }
  .btn {
    display: inline-block;
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
  }
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .danger {
    border-color: var(--crit);
    color: var(--crit);
  }
  .banner {
    padding: var(--space-3) var(--space-4);
    border-radius: var(--radius-m);
    border: 1px solid var(--border);
    background: var(--surface);
    margin-bottom: var(--space-4);
  }
  .banner.crit {
    border-color: var(--crit);
  }
  .banner p {
    margin: 0 0 var(--space-2);
  }
  label {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: center;
    font-size: 13px;
  }
  input[type='text'] {
    font: inherit;
    font-size: 13px;
    padding: var(--space-1) var(--space-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    background: var(--bg);
    color: var(--text);
    min-inline-size: 16ch;
  }
  .group {
    margin-bottom: var(--space-4);
  }
  .count {
    color: var(--text-muted);
    font-weight: normal;
  }
  .table {
    overflow-x: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
  }
  table {
    inline-size: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th,
  td {
    padding: var(--space-2) var(--space-3);
    border-block-end: 1px solid var(--border);
    text-align: start;
    vertical-align: top;
  }
  tr:last-child td {
    border-block-end: none;
  }
  td.pick {
    inline-size: 2rem;
  }
  td.n {
    text-align: end;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  td.wrap {
    overflow-wrap: anywhere;
  }
  .path {
    font-family: var(--font-mono);
    unicode-bidi: plaintext;
    display: block;
  }
  .tag {
    display: inline-block;
    font-size: 12px;
    padding: 0 var(--space-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    margin-block-start: var(--space-1);
  }
  .tag.crit {
    border-color: var(--crit);
    color: var(--crit);
  }
  .typed {
    margin-block-start: var(--space-1);
  }
  .footer {
    position: sticky;
    inset-block-end: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: center;
    justify-content: flex-end;
    padding: var(--space-3) var(--space-4);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
  }
  .footer strong {
    margin-inline-end: auto;
  }
  .result ul {
    padding-inline-start: var(--space-4);
  }
  .failed strong {
    color: var(--crit);
  }
</style>

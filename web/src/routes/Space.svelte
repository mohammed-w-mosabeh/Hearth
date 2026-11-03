<script lang="ts">
  import { failure } from '../lib/errors';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { api } from '../lib/api';
  import { bytes } from '../lib/format';
  import { locale, t } from '../lib/i18n';
  import { MAX_SELECTED, freeable, pathParams, toggled, type Entry, type Large, type Report } from '../lib/space';
  import { href } from '../lib/router.svelte';
  import { toast } from '../lib/toast.svelte';

  // Actions confirmed through the same dialog; each previews first.
  interface Pending {
    kind: string;
    action: string;
    target: string;
    params: Record<string, string>;
    plan: Plan;
    title: string;
    verb: string;
    body: string;
    after?: () => void;
  }

  let report = $state.raw<Report | null>(null);
  let failed = $state('');
  let loading = $state(false);
  let docker = $state<number | null>(null);
  let keep = $state(500);
  let cache = $state.raw<string[]>([]);
  let large = $state.raw<Large | null>(null);
  let finding = $state(false);
  let chosen = $state.raw<string[]>([]);
  let pending = $state<Pending | null>(null);

  async function load(): Promise<void> {
    loading = true;
    try {
      report = await api<Report>('/space');
      failed = '';
    } catch (err) {
      failed = failure(err, 'space.error');
    } finally {
      loading = false;
    }
    try {
      const d = await api<{ kinds: { reclaimable: number }[] }>('/docker/cleanup');
      docker = d.kinds.reduce((sum, k) => sum + k.reclaimable, 0);
    } catch {
      docker = null;
    }
  }

  $effect(() => {
    void load();
  });

  function report_(res: Result): void {
    const first = res.failed[0];
    if (first) {
      toast(t('space.failed', { n: res.failed.length, why: t(reasonKey(first.reason), { detail: first.detail ?? '' }) }), { tone: 'error' });
    } else {
      toast(t('space.done'));
    }
  }

  function toastFailure(err: unknown): void {
    toast(failure(err, 'action.failed'), { tone: 'error' });
  }

  async function ask(p: Omit<Pending, 'plan'>): Promise<void> {
    try {
      const plan = await preview(p.kind, p.action, p.target, p.params);
      if (plan.items.length === 0 && plan.blocked.length === 0) {
        toast(t('space.nothing'));
        await load();
        return;
      }
      pending = { ...p, plan };
    } catch (err) {
      toastFailure(err);
    }
  }

  async function confirmed(typed: string): Promise<void> {
    const c = pending;
    pending = null;
    if (!c) return;
    try {
      report_(await run(c.kind, c.action, c.target, c.params, c.plan.token, typed));
      c.after?.();
      await load();
    } catch (err) {
      toastFailure(err);
    }
  }

  async function findLarge(): Promise<void> {
    finding = true;
    chosen = [];
    try {
      large = await api<Large>('/space/large');
    } catch (err) {
      toastFailure(err);
    } finally {
      finding = false;
    }
  }

  function trashCache(): Promise<void> {
    return ask({
      kind: 'space', action: 'trash', target: 'cache', params: pathParams(cache), title: t('space.toTrash.confirm'), verb: t('space.toTrash'),
      body: t('space.toTrash.body'), after: () => (cache = []),
    });
  }

  function trashLarge(): Promise<void> {
    return ask({
      kind: 'space', action: 'trash', target: 'large', params: pathParams(chosen), title: t('space.toTrash.confirm'), verb: t('space.toTrash'),
      body: t('space.toTrash.body'),
      after: () => {
        const gone = new Set(chosen);
        if (large) large = { ...large, items: large.items.filter((e) => !gone.has(e.path)) };
        chosen = [];
      },
    });
  }

  const when = (s: number) => new Date(s * 1000).toLocaleDateString(locale());
  const total = $derived(report ? freeable(report, keep, docker ?? 0) : 0);
</script>

{#snippet picker(items: Entry[], selected: string[], set: (v: string[]) => void, canPick: boolean)}
  <div class="table">
    <table>
      <tbody>
        {#each items as e (e.path)}
          <tr>
            {#if canPick}
              <td class="pick"><input type="checkbox" aria-label={e.name} checked={selected.includes(e.path)} onchange={() => set(toggled(selected, e.path))} disabled={!selected.includes(e.path) && selected.length >= MAX_SELECTED} /></td>
            {/if}
            <td class="wrap">{e.time ? e.path : e.name}</td>
            {#if e.time}<td class="n">{when(e.time)}</td>{/if}
            <td class="n">{bytes(e.size)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

<header>
  <div>
    <h1>{t('space.title')}</h1>
    <p>{t('space.intro')}</p>
  </div>
  <button type="button" class="btn" onclick={() => load()} disabled={loading}>{t('space.refresh')}</button>
</header>

{#if failed}
  <p class="note error" role="status">{failed}</p>
{:else if report === null}
  <p class="note">{t('space.loading')}</p>
{:else}
  {@const r = report}
  <section class="summary" aria-busy={loading}><strong>{t('space.total', { size: bytes(total) })}</strong></section>

  <div class="cards">
    <article>
      <div class="head"><h2>{t('space.apt.title')}</h2><span class="size">{bytes(r.apt.size)}</span></div>
      <p class="note">{t('space.apt.hint')}</p>
      <div class="row">
        <button type="button" class="btn" disabled={r.apt.count === 0}
          onclick={() => ask({ kind: 'space', action: 'apt-clean', target: 'apt', params: {}, title: t('space.apt.confirm'), verb: t('space.apt.action'), body: t('space.apt.body') })}>{t('space.apt.action')}</button>
      </div>
    </article>

    <article>
      <div class="head"><h2>{t('space.journal.title')}</h2><span class="size">{bytes(r.journal.size)}</span></div>
      <p class="note">{t('space.journal.hint')}</p>
      <div class="row">
        <label class="inline">{t('space.journal.keep')}
          <select bind:value={keep}>
            {#each r.journal_keep as k (k)}<option value={k}>{t('space.journal.mib', { n: k })}</option>{/each}
          </select>
        </label>
        <button type="button" class="btn"
          onclick={() => ask({ kind: 'space', action: 'journal-vacuum', target: String(keep), params: {}, title: t('space.journal.confirm'), verb: t('space.journal.action'), body: t('space.journal.body') })}>{t('space.journal.action')}</button>
      </div>
    </article>

    <article>
      <div class="head"><h2>{t('space.docker.title')}</h2><span class="size">{docker === null ? '—' : bytes(docker)}</span></div>
      <p class="note">{docker === null ? t('space.docker.none') : t('space.docker.hint')}</p>
      {#if docker !== null}<div class="row"><a class="btn" href={href('docker')}>{t('space.docker.open')}</a></div>{/if}
    </article>

    <article>
      <div class="head"><h2>{t('space.trash.title')}</h2><span class="size">{bytes(r.trash.size)}</span></div>
      <p class="note">{t('space.trash.hint')}</p>
      <div class="row">
        <button type="button" class="btn" disabled={r.trash.count === 0 || !r.can_trash}
          onclick={() => ask({ kind: 'space', action: 'empty-trash', target: 'trash', params: {}, title: t('space.trash.confirm'), verb: t('space.trash.action'), body: t('space.trash.body') })}>{t('space.trash.action')}</button>
      </div>
    </article>

    <article class="wide">
      <div class="head"><h2>{t('space.kernels.title')}</h2><span class="size">{bytes(r.kernels.reduce((s, k) => s + k.size, 0))}</span></div>
      <p class="note">{t('space.kernels.hint', { running: r.running })}</p>
      {#if r.kernels.length === 0}
        <p class="note">{t('space.kernels.none')}</p>
      {:else}
        <div class="table">
          <table>
            <tbody>
              {#each r.kernels as k (k.package)}
                <tr>
                  <td class="wrap">{k.package}</td>
                  <td class="n">{bytes(k.size)}</td>
                  <td class="n"><button type="button" class="btn"
                    onclick={() => ask({ kind: 'package', action: 'remove', target: k.package, params: {}, title: k.package, verb: t('space.remove'), body: t('space.kernels.body') })}>{t('space.remove')}</button></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </article>

    <article class="wide">
      <div class="head"><h2>{t('space.cache.title')}</h2><span class="size">{bytes(r.cache.size)}</span></div>
      <p class="note">{t('space.cache.hint')}</p>
      {#if !r.can_trash}<p class="note error">{t('space.noGio')}</p>{/if}
      {#if r.cache.items?.length}
        {@render picker(r.cache.items, cache, (v) => (cache = v), r.can_trash)}
        <div class="row">
          <button type="button" class="btn" disabled={cache.length === 0} onclick={trashCache}>{t('space.toTrash')}</button>
          {#if cache.length > 0}<span class="note">{t('space.selected', { n: cache.length, max: MAX_SELECTED })}</span>{/if}
        </div>
      {:else}
        <p class="note">{t('space.nothing')}</p>
      {/if}
    </article>

    <article class="wide">
      <div class="head"><h2>{t('space.large.title')}</h2></div>
      <p class="note">{t('space.large.hint')}</p>
      <div class="row">
        <button type="button" class="btn" onclick={findLarge} disabled={finding} aria-busy={finding}>{finding ? t('space.large.running') : t('space.large.run')}</button>
        {#if chosen.length > 0}
          <button type="button" class="btn" onclick={trashLarge}>{t('space.toTrash')}</button>
          <span class="note">{t('space.selected', { n: chosen.length, max: MAX_SELECTED })}</span>
        {/if}
      </div>
      {#if large}
        {#if large.truncated}<p class="note">{t('space.large.truncated')}</p>{/if}
        {#if large.items.length === 0}
          <p class="note">{t('space.large.none')}</p>
        {:else}
          {@render picker(large.items, chosen, (v) => (chosen = v), r.can_trash)}
        {/if}
      {/if}
    </article>
  </div>
{/if}

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending?.title ?? ''}
  verb={pending?.verb ?? ''}
  body={pending?.body ?? ''}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
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
    margin: 0;
  }
  header p,
  .note {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
    font-size: 13px;
  }
  header p {
    font-size: inherit;
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
  .summary {
    padding: var(--space-3) var(--space-4);
    border-radius: var(--radius-m);
    border: 1px solid var(--border);
    background: var(--surface);
    margin-bottom: var(--space-4);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 320px), 1fr));
    gap: var(--space-4);
    align-items: start;
  }
  article {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    min-width: 0;
  }
  .wide {
    grid-column: 1 / -1;
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: var(--space-2);
    align-items: baseline;
  }
  .size {
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: center;
    margin-top: var(--space-2);
  }
  .row .note {
    margin: 0;
  }
  .inline {
    display: inline-flex;
    gap: var(--space-2);
    align-items: center;
    font-size: 13px;
    color: var(--text-muted);
  }
  select {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 2px var(--space-2);
  }
  .table {
    max-height: 320px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    margin-top: var(--space-2);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  td {
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border);
  }
  .wrap {
    overflow-wrap: anywhere;
  }
  .n {
    text-align: end;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  .pick {
    width: 1.5em;
  }
</style>

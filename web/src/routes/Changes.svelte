<script lang="ts">
  import { failure } from '../lib/errors';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import type { Plan } from '../lib/actions';
  import { api } from '../lib/api';
  import {
    SOURCES,
    byDay,
    diffEmpty,
    diffRows,
    entryDetail,
    eventDetail,
    eventKey,
    sourceKey,
    timelineURL,
    toggled,
    type DiffRow,
    type Snapshot,
    type SnapshotDiff,
    type Timeline,
    type TimelineEvent,
  } from '../lib/changes';
  import { bytes } from '../lib/format';
  import { locale, t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  type View = 'timeline' | 'snapshots';

  // Package lists can differ by thousands of entries.
  const SHOW = 300;
  const MARK: Record<DiffRow['mark'], string> = { added: '+', removed: '−', changed: '~' };

  let view = $state<View>('timeline');
  let events = $state.raw<TimelineEvent[] | null>(null);
  let next = $state<number | undefined>();
  let filter = $state.raw<string[]>([]);
  let failed = $state('');
  let loading = $state(false);

  let snapshots = $state.raw<Snapshot[]>([]);
  let snapshotsFailed = $state('');
  let label = $state('');
  let taking = $state(false);
  let from = $state('');
  let to = $state('now');
  let diff = $state.raw<SnapshotDiff | null>(null);
  let comparing = $state(false);
  let pending = $state<{ snapshot: Snapshot; plan: Plan } | null>(null);

  // The filter can change again before the previous page arrives.
  let loadSeq = 0;

  async function load(before?: number): Promise<void> {
    const seq = ++loadSeq;
    loading = true;
    try {
      const page = await api<Timeline>(timelineURL(filter, before));
      if (seq !== loadSeq) return;
      events = before === undefined ? page.events : [...(events ?? []), ...page.events];
      next = page.next;
      failed = '';
    } catch (err) {
      if (seq === loadSeq) failed = failure(err, 'changes.error');
    } finally {
      if (seq === loadSeq) loading = false;
    }
  }

  async function loadSnapshots(): Promise<void> {
    try {
      snapshots = await api<Snapshot[]>('/changes/snapshots');
      snapshotsFailed = '';
      if (from === '' && snapshots[0]) from = String(snapshots[0].id);
    } catch (err) {
      snapshotsFailed = failure(err, 'changes.snap.error');
    }
  }

  $effect(() => {
    void load();
    void loadSnapshots();
  });

  function setFilter(source: string): void {
    filter = toggled(filter, source);
    void load();
  }

  async function take(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    taking = true;
    try {
      const s = await api<Snapshot>('/changes/snapshots', { method: 'POST', body: JSON.stringify({ label: label.trim() }) });
      toast(t('changes.snap.taken', { s: s.label }));
      label = '';
      from = String(s.id);
      diff = null;
      await Promise.all([loadSnapshots(), load()]);
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      taking = false;
    }
  }

  async function compare(): Promise<void> {
    comparing = true;
    try {
      diff = await api<SnapshotDiff>(`/changes/diff?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`);
    } catch (err) {
      diff = null;
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      comparing = false;
    }
  }

  function askDelete(snapshot: Snapshot): void {
    pending = { snapshot, plan: { summary: '', items: [{ id: String(snapshot.id), name: snapshot.label }], blocked: [], risk: 'destructive' } };
  }

  async function confirmed(): Promise<void> {
    const s = pending?.snapshot;
    pending = null;
    if (!s) return;
    try {
      await api(`/changes/snapshots/${s.id}`, { method: 'DELETE' });
      if (from === String(s.id)) from = '';
      if (to === String(s.id)) to = 'now';
      diff = null;
      await Promise.all([loadSnapshots(), load()]);
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  function title(e: TimelineEvent): string {
    const key = eventKey(e);
    return key ? t(key) : (e.action ?? `${e.source} ${e.kind}`);
  }

  const when = (iso: string) => new Date(iso).toLocaleString(locale());
  const day = (iso: string) => new Date(iso).toLocaleDateString(locale(), { dateStyle: 'full' });
  const hour = (iso: string) => new Date(iso).toLocaleTimeString(locale());
  const snapshotName = (s: Snapshot) => `${when(s.t)} — ${s.label || t('changes.snap.auto')}`;

  const days = $derived(byDay(events ?? [], day));
  const kept = $derived(snapshots.filter((s) => !s.auto));
</script>

<header>
  <h1>{t('changes.title')}</h1>
  <p>{t('changes.intro')}</p>
</header>

<div class="segmented" role="radiogroup" aria-label={t('changes.title')}>
  {#each ['timeline', 'snapshots'] as const as v (v)}
    <button type="button" role="radio" aria-checked={view === v} class:on={view === v} onclick={() => (view = v)}>
      {t(v === 'timeline' ? 'changes.view.timeline' : 'changes.view.snapshots')}
    </button>
  {/each}
</div>

{#if view === 'timeline'}
  <div class="filters" role="group" aria-label={t('changes.filter')}>
    {#each SOURCES as s (s)}
      <label class="toggle"><input type="checkbox" checked={filter.includes(s)} onchange={() => setFilter(s)} /> {t(sourceKey(s))}</label>
    {/each}
  </div>

  {#if failed}
    <p class="note error" role="status">{failed}</p>
  {:else if events === null}
    <p class="note">{t('changes.loading')}</p>
  {:else if events.length === 0}
    <p class="note">{t('changes.none')}</p>
  {:else}
    <div aria-busy={loading}>
      {#each days as d (d.day)}
        <section class="day">
          <h2>{d.day}</h2>
          <ul>
            {#each d.events as e, i (i)}
              <li>
                <time datetime={e.t}>{hour(e.t)}</time>
                <span class="what">{title(e)}</span>
                <span class="subject">{e.subject}</span>
                <span class="detail">{eventDetail(e)}</span>
              </li>
            {/each}
          </ul>
        </section>
      {/each}
    </div>
    {#if next !== undefined}
      <button type="button" class="btn more" onclick={() => load(next)} disabled={loading}>{t('changes.older')}</button>
    {/if}
  {/if}
{:else}
  <form class="row" onsubmit={take}>
    <label class="field">
      <span>{t('changes.snap.label')}</span>
      <input type="text" bind:value={label} maxlength="80" required placeholder={t('changes.snap.placeholder')} />
    </label>
    <button type="submit" class="btn" disabled={taking || label.trim() === ''} aria-busy={taking}>
      {taking ? t('changes.snap.taking') : t('changes.snap.take')}
    </button>
  </form>
  <p class="note">{t('changes.snap.hint')}</p>

  {#if snapshotsFailed}
    <p class="note error" role="status">{snapshotsFailed}</p>
  {:else if snapshots.length === 0}
    <p class="note">{t('changes.snap.none')}</p>
  {:else}
    {#if kept.length > 0}
      <h2>{t('changes.snap.kept')}</h2>
      <ul class="kept">
        {#each kept as s (s.id)}
          <li>
            <span class="label">{s.label}</span>
            <span class="note">{when(s.t)} · {bytes(s.size)}</span>
            <button type="button" class="btn" onclick={() => askDelete(s)}>{t('changes.snap.delete')}</button>
          </li>
        {/each}
      </ul>
    {/if}

    <h2>{t('changes.diff.title')}</h2>
    <div class="row">
      <label class="field">
        <span>{t('changes.diff.from')}</span>
        <select bind:value={from}>
          {#each snapshots as s (s.id)}<option value={String(s.id)}>{snapshotName(s)}</option>{/each}
        </select>
      </label>
      <label class="field">
        <span>{t('changes.diff.to')}</span>
        <select bind:value={to}>
          <option value="now">{t('changes.diff.now')}</option>
          {#each snapshots as s (s.id)}<option value={String(s.id)}>{snapshotName(s)}</option>{/each}
        </select>
      </label>
      <button type="button" class="btn" onclick={compare} disabled={comparing || from === ''} aria-busy={comparing}>{t('changes.diff.run')}</button>
    </div>

    {#if diff}
      <p class="note">{t('changes.diff.between', { from: when(diff.from), to: when(diff.to) })}</p>
      {#if diffEmpty(diff.sections)}
        <p class="note">{t('changes.diff.same')}</p>
      {/if}
      {#each diff.sections as s (s.source)}
        {@const rows = diffRows(s)}
        {#if s.missing}
          <section class="diff">
            <h3>{t(sourceKey(s.source))}</h3>
            <p class="note">{t('changes.diff.missing')}</p>
          </section>
        {:else if rows.length > 0}
          <section class="diff">
            <h3>{t(sourceKey(s.source))} <span class="note">{t('changes.diff.counts', { added: s.added.length, removed: s.removed.length, changed: s.changed.length })}</span></h3>
            <ul>
              {#each rows.slice(0, SHOW) as r (r.mark + r.key)}
                <li class={r.mark}>
                  <span class="mark" aria-hidden="true">{MARK[r.mark]}</span>
                  <span class="subject">{r.key}</span>
                  <span class="detail">{entryDetail(r)}</span>
                </li>
              {/each}
            </ul>
            {#if rows.length > SHOW}
              <p class="note">{t('changes.diff.more', { n: SHOW })}</p>
            {/if}
          </section>
        {/if}
      {/each}
    {/if}
  {/if}
{/if}

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('changes.snap.confirm', { s: pending.snapshot.label }) : ''}
  verb={t('changes.snap.delete')}
  body={t('changes.snap.confirmBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  h2 {
    font-size: 15px;
    margin: var(--space-4) 0 var(--space-2);
  }
  h3 {
    font-size: 14px;
    margin: 0 0 var(--space-2);
  }
  header p,
  .note {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  h3 .note {
    font-weight: 400;
    font-size: 12px;
  }
  .error {
    color: var(--crit);
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
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .more {
    margin-top: var(--space-3);
  }
  .segmented {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow: hidden;
    margin-bottom: var(--space-3);
  }
  .segmented button {
    font: inherit;
    font-size: 13px;
    color: var(--text-muted);
    background: var(--surface);
    border: 0;
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .segmented button.on {
    color: var(--text);
    background: var(--surface-2);
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    margin-bottom: var(--space-2);
  }
  .toggle {
    display: inline-flex;
    gap: var(--space-1);
    align-items: center;
    font-size: 13px;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .day li {
    display: grid;
    grid-template-columns: 7em minmax(9em, 1fr) minmax(0, 2fr) minmax(0, 2fr);
    gap: var(--space-2);
    padding: var(--space-1) 0;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  @media (max-width: 720px) {
    .day li {
      grid-template-columns: 1fr;
      gap: 0;
    }
  }
  time,
  .detail {
    color: var(--text-muted);
  }
  time {
    font-variant-numeric: tabular-nums;
  }
  .subject {
    font-family: var(--font-mono);
    font-size: 12px;
    min-width: 0;
  }
  .detail {
    font-size: 12px;
    min-width: 0;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: flex-end;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 13px;
    color: var(--text-muted);
    min-width: min(100%, 260px);
    flex: 1 1 260px;
  }
  input[type='text'],
  select {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
    min-width: 0;
  }
  .kept li {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: center;
    padding: var(--space-1) 0;
    border-bottom: 1px solid var(--border);
  }
  .kept .label {
    flex: 1 1 12em;
    overflow-wrap: anywhere;
  }
  .kept .note {
    margin: 0;
    font-size: 12px;
  }
  .diff {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-top: var(--space-3);
  }
  .diff ul {
    max-height: 420px;
    overflow: auto;
  }
  .diff li {
    display: grid;
    grid-template-columns: 1.5em minmax(0, 1fr) minmax(0, 1fr);
    gap: var(--space-2);
    padding: 2px 0;
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .mark {
    font-family: var(--font-mono);
    color: var(--warn);
  }
  .added .mark {
    color: var(--ok);
  }
  .removed .mark {
    color: var(--crit);
  }
</style>

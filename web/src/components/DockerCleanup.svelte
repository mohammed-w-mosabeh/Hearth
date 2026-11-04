<script lang="ts">
  import { failure } from '../lib/errors';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Plan } from '../lib/actions';
  import { api } from '../lib/api';
  import { pruneTotal, type KindSummary, type PruneKind } from '../lib/docker';
  import { bytes } from '../lib/format';
  import { t, type MessageKey } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  // Cleanup (ROADMAP 2.8): what Docker could reclaim, by kind. Removing is
  // always previewed (SECURITY S15): the dialog lists exactly what goes,
  // and the broker removes nothing newer than the preview.
  let kinds = $state.raw<KindSummary[] | null>(null);
  let failed = $state('');
  let loading = $state(false);
  let pending = $state<{ kind: PruneKind; plan: Plan } | null>(null);
  let busy = $state<PruneKind | null>(null);

  async function load(): Promise<void> {
    loading = true;
    try {
      kinds = (await api<{ kinds: KindSummary[] }>('/docker/cleanup')).kinds;
      failed = '';
    } catch (err) {
      failed = failure(err, 'action.failed');
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void load();
  });

  const total = $derived(kinds ? pruneTotal(kinds) : 0);

  const labels: Record<PruneKind, { title: MessageKey; body: MessageKey }> = {
    containers: { title: 'cleanup.containers', body: 'cleanup.containers.body' },
    images: { title: 'cleanup.images', body: 'cleanup.images.body' },
    networks: { title: 'cleanup.networks', body: 'cleanup.networks.body' },
    volumes: { title: 'cleanup.volumes', body: 'cleanup.volumes.body' },
    build: { title: 'cleanup.build', body: 'cleanup.build.body' },
  };

  async function review(kind: PruneKind): Promise<void> {
    busy = kind;
    try {
      const plan = await preview('prune', kind, 'docker');
      if (plan.items.length === 0) {
        toast(t('cleanup.nothing'));
        await load();
        return;
      }
      pending = { kind, plan };
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = null;
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { kind, plan } = pending;
    pending = null;
    busy = kind;
    toast(t('docker.authHint'));
    try {
      const res = await run('prune', kind, 'docker', {}, plan.token, typed);
      const bad = res.failed[0];
      if (bad && res.done === 0) {
        const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
        toast(t('cleanup.failed', { kind: t(labels[kind].title), reason }), { tone: 'error' });
      } else {
        toast(t('cleanup.done', { n: res.done, kept: res.failed.length }));
      }
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = null;
      await load();
    }
  }
</script>

<div class="bar">
  <span class="total">{kinds ? t('cleanup.total', { size: bytes(total) }) : t('cleanup.loading')}</span>
  <button type="button" class="plain" disabled={loading} onclick={load}>{loading ? t('cleanup.loading') : t('cleanup.refresh')}</button>
</div>
{#if failed}<p class="note error" role="status">{failed}</p>{/if}

{#if kinds}
  <div class="grid">
    {#each kinds as k (k.kind)}
      <section class="card" aria-labelledby="kind-{k.kind}">
        <header>
          <h3 id="kind-{k.kind}">{t(labels[k.kind].title)}</h3>
          <span class="size">{k.error ? '' : k.reclaimable > 0 ? bytes(k.reclaimable) : ''}</span>
        </header>
        <p class="muted">{t(labels[k.kind].body)}</p>
        {#if k.error}
          <p class="note error">{k.error}</p>
        {:else if k.count === 0}
          <p class="note">{t('cleanup.none')}</p>
        {:else}
          <ul>
            {#each k.items.slice(0, 5) as it (it.id)}
              <li><span class="item" title={it.id}>{it.name || it.id.replace(/^sha256:/, '').slice(0, 12)}</span><span class="muted nowrap">{it.size > 0 || (it.size === 0 && k.kind !== 'networks') ? bytes(it.size) : ''}</span></li>
            {/each}
            {#if k.count > 5}<li class="muted">{t('apps.more', { n: k.count - 5 })}</li>{/if}
          </ul>
          <button type="button" class="plain danger" disabled={busy !== null} onclick={() => review(k.kind)}>
            {busy === k.kind ? '…' : t('cleanup.review', { n: k.count })}
          </button>
        {/if}
      </section>
    {/each}
  </div>
  <p class="foot">{t('cleanup.footnote')}</p>
{/if}

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('cleanup.confirmTitle', { kind: t(labels[pending.kind].title) }) : ''}
  verb={t('image.op.remove')}
  {...pending?.kind === 'volumes' ? { warning: t('cleanup.volumesWarning') } : {}}
  body={t('cleanup.confirmBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  .total {
    font-size: 15px;
    font-weight: 600;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: var(--space-3);
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    padding: var(--space-3) var(--space-4);
    font-size: 13px;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
  }
  h3 {
    font-size: 14px;
  }
  .size {
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  p {
    margin: 0;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    flex: 1;
  }
  li {
    display: flex;
    justify-content: space-between;
    gap: var(--space-3);
    padding: 2px 0;
  }
  .nowrap {
    white-space: nowrap;
    flex-shrink: 0;
  }
  .item {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .note,
  .muted,
  .foot {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .foot {
    font-size: 12px;
    margin-top: var(--space-3);
  }
  .plain {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    align-self: flex-start;
  }
  .plain.danger {
    color: var(--crit);
    border-color: var(--crit);
  }
  .plain:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>

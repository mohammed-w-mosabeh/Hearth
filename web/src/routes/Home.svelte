<script lang="ts">
  import { failure } from '../lib/errors';
  import HomeHealth from '../components/HomeHealth.svelte';
  import HomeTiles from '../components/HomeTiles.svelte';
  import CpuCard from '../components/cards/CpuCard.svelte';
  import DockerCard from '../components/cards/DockerCard.svelte';
  import DiskCard from '../components/cards/DiskCard.svelte';
  import GpuCard from '../components/cards/GpuCard.svelte';
  import MemCard from '../components/cards/MemCard.svelte';
  import NetCard from '../components/cards/NetCard.svelte';
  import PowerCard from '../components/cards/PowerCard.svelte';
  import TempsCard from '../components/cards/TempsCard.svelte';
  import { api } from '../lib/api';
  import type { UsageMsg } from '../lib/docker';
  import { t } from '../lib/i18n';
  import { arrange, moveTo, sanitize, setHidden, step, type Layout } from '../lib/layout';
  import { LiveMetrics } from '../lib/metrics.svelte';
  import type { Disk, GPU, Sample } from '../lib/types';
  import type { Live } from '../lib/ws';

  let { live }: { live: Live } = $props();

  type Card =
    | { id: string; kind: 'cpu' | 'mem' | 'temps' | 'net' | 'power' | 'docker'; title: string }
    | { id: string; kind: 'disk'; title: string; disk: Disk }
    | { id: string; kind: 'gpu'; title: string; gpu: GPU };

  // Cards in their natural order; ids are stable across restarts (mount
  // point, PCI address) so the saved layout keeps applying.
  function cardsFor(s: Sample, docker: boolean): Card[] {
    const cards: Card[] = [
      { id: 'cpu', kind: 'cpu', title: t('card.cpu') },
      { id: 'mem', kind: 'mem', title: t('card.mem') },
    ];
    for (const d of s.disks ?? []) cards.push({ id: `disk:${d.mount}`, kind: 'disk', title: t('card.disk', { mount: d.mount }), disk: d });
    const vendors: Record<string, string> = { intel: 'Intel', amd: 'AMD', nvidia: 'NVIDIA' };
    for (const g of s.gpus ?? []) {
      const vendor = vendors[g.vendor];
      const title = vendor ? t('card.gpuVendor', { vendor }) : t('card.gpu');
      cards.push({ id: `gpu:${g.pci}`, kind: 'gpu', title, gpu: g });
    }
    if ((s.temps ?? []).length > 0 || s.cpu_temp_c !== undefined) cards.push({ id: 'temps', kind: 'temps', title: t('card.temps') });
    const p = s.power;
    if ((p?.batteries ?? []).length > 0 || p?.profile) {
      cards.push({ id: 'power', kind: 'power', title: (p.batteries ?? []).length > 0 ? t('card.battery') : t('card.power') });
    }
    cards.push({ id: 'net', kind: 'net', title: t('card.net') });
    if (docker) cards.push({ id: 'docker', kind: 'docker', title: t('card.docker') });
    return cards;
  }

  // Containers' usage from hearthd's cgroup reader; following the topic
  // makes it read every 2 s instead of 15 s, without waking the broker.
  let usage = $state.raw<UsageMsg | null>(null);
  $effect(() => {
    api<UsageMsg>('/docker/usage')
      .then((u) => (usage ??= u))
      .catch(() => {});
    return live.subscribe('docker.usage', (data) => {
      const u = data as UsageMsg;
      if (u && Array.isArray(u.running)) usage = u;
    });
  });

  let metrics = $state.raw<LiveMetrics | null>(null);
  $effect(() => {
    const m = new LiveMetrics(live);
    metrics = m;
    return () => m.close();
  });

  // Layout: loaded once, saved (debounced) after each change.
  let layout = $state<Layout>({ order: [], hidden: [] });
  let loaded = $state(false);
  let saveFailed = $state('');
  let editing = $state(false);
  let saveTimer: ReturnType<typeof setTimeout> | undefined;

  $effect(() => {
    api<unknown>('/settings/home.layout')
      .then((v) => (layout = sanitize(v)))
      .catch(() => {})
      .finally(() => (loaded = true));
    return () => clearTimeout(saveTimer);
  });

  function update(next: Layout): void {
    if (next === layout) return;
    layout = next;
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => {
      api('/settings/home.layout', { method: 'PUT', body: JSON.stringify($state.snapshot(layout)) })
        .then(() => (saveFailed = ''))
        .catch((err: unknown) => (saveFailed = failure(err, 'home.saveFailed')));
    }, 400);
  }

  const sample = $derived(metrics?.latest ?? null);
  const cards = $derived(sample ? cardsFor(sample, usage?.available === true) : []);
  const byId = $derived(new Map(cards.map((c) => [c.id, c])));
  const arranged = $derived(arrange(layout, cards.map((c) => c.id)));

  // Drag and drop (pointer); the arrow buttons do the same from the keyboard.
  let dragging = $state<string | null>(null);
  let over = $state<string | null>(null);

  function onDrop(target: string | null): void {
    if (dragging) update(moveTo(layout, arranged.visible, dragging, target));
    dragging = over = null;
  }
</script>

<header>
  <div>
    <h1>{t('home.title')}</h1>
    <p>{editing ? t('home.customizeHint') : t('home.intro')}</p>
  </div>
  {#if sample && loaded}
    <button type="button" class:primary={editing} aria-pressed={editing} onclick={() => (editing = !editing)}>
      {editing ? t('home.done') : t('home.customize')}
    </button>
  {/if}
</header>

<HomeHealth {live} />

<HomeTiles {live} />

{#if saveFailed}
  <p class="error" role="status">{saveFailed}</p>
{/if}

{#if !metrics || !sample || !loaded}
  <p class="waiting">{t('home.waiting')}</p>
{:else}
  <h2 class="section">{t('home.quickViews')}</h2>
  <ul class="grid" class:editing aria-label={t('home.cards')}>
    {#each arranged.visible as id, i (id)}
      {@const card = byId.get(id)}
      {#if card}
        <li
          class="card"
          class:dragging={dragging === id}
          class:over={over === id && dragging !== id}
          draggable={editing}
          ondragstart={(e) => {
            dragging = id;
            e.dataTransfer?.setData('text/plain', id);
            if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move';
          }}
          ondragover={(e) => {
            if (!dragging) return;
            e.preventDefault();
            over = id;
          }}
          ondragleave={() => {
            if (over === id) over = null;
          }}
          ondrop={(e) => {
            e.preventDefault();
            onDrop(id);
          }}
          ondragend={() => (dragging = over = null)}
        >
          <div class="head">
            <h2 title={card.title}>{card.title}</h2>
            {#if editing}
              <div class="tools">
                <button type="button" class="icon" disabled={i === 0} aria-label={t('home.moveEarlier', { name: card.title })}
                  onclick={() => update(step(layout, arranged.visible, id, -1))}>{t('arrow.earlier')}</button>
                <button type="button" class="icon" disabled={i === arranged.visible.length - 1} aria-label={t('home.moveLater', { name: card.title })}
                  onclick={() => update(step(layout, arranged.visible, id, 1))}>{t('arrow.later')}</button>
                <button type="button" class="icon" aria-label={t('home.hide', { name: card.title })}
                  onclick={() => update(setHidden(layout, id, true))}>✕</button>
              </div>
            {/if}
          </div>
          {#if card.kind === 'cpu'}
            <CpuCard {sample} {metrics} />
          {:else if card.kind === 'mem'}
            <MemCard {sample} {metrics} />
          {:else if card.kind === 'disk'}
            <DiskCard disk={card.disk} />
          {:else if card.kind === 'gpu'}
            <GpuCard gpu={card.gpu} {metrics} />
          {:else if card.kind === 'temps'}
            <TempsCard {sample} {metrics} />
          {:else if card.kind === 'power'}
            <PowerCard power={sample.power} {metrics} />
          {:else if card.kind === 'net'}
            <NetCard {sample} {metrics} />
          {:else if card.kind === 'docker' && usage}
            <DockerCard {usage} />
          {/if}
        </li>
      {/if}
    {/each}
  </ul>

  {#if editing && arranged.hidden.length > 0}
    <section class="hidden-cards">
      <h2>{t('home.hiddenCards')}</h2>
      <ul>
        {#each arranged.hidden as id (id)}
          <li>
            <button type="button" onclick={() => update(setHidden(layout, id, false))}>
              + {byId.get(id)?.title ?? id}
            </button>
          </li>
        {/each}
      </ul>
    </section>
  {/if}
{/if}

<style>
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--space-4);
    margin-bottom: var(--space-5);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
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
  button:hover:not(:disabled) {
    background: var(--surface-2);
  }
  button:disabled {
    opacity: 0.4;
    cursor: default;
  }
  button.primary {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-contrast);
  }
  button.icon {
    padding: 0 var(--space-2);
    line-height: 22px;
  }
  .waiting,
  .error {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .section {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: var(--space-5) 0 var(--space-3);
  }
  .grid {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: var(--space-4);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    padding: var(--space-4);
    min-width: 0;
    transition:
      opacity var(--motion),
      border-color var(--motion);
  }
  .editing .card {
    cursor: grab;
    border-style: dashed;
  }
  .card.dragging {
    opacity: 0.4;
  }
  .card.over {
    border-color: var(--accent);
    border-style: solid;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-3);
    min-height: 24px;
  }
  .head h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tools {
    display: flex;
    gap: var(--space-1);
    flex-shrink: 0;
  }
  .hidden-cards {
    margin-top: var(--space-5);
  }
  .hidden-cards h2 {
    font-size: 14px;
    margin-bottom: var(--space-2);
  }
  .hidden-cards ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
</style>

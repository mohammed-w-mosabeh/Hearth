<script lang="ts">
  import EmptyState from './EmptyState.svelte';
  import { failure } from '../lib/errors';
  import Icon from './Icon.svelte';
  import TileForm from './TileForm.svelte';
  import { reasonKey, run } from '../lib/actions';
  import { api } from '../lib/api';
  import type { Container, Snapshot } from '../lib/docker';
  import { builtinName, emptyForm, formOf, groupTiles, hostOf, moved, suggestionForm, type HomepageMsg, type Status, type Suggestion, type Tile, type TileForm as Form } from '../lib/homepage';
  import { locale, t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // Homepage tiles (SPEC 6, ROADMAP 2.9). Following "homepage" is what
  // makes hearthd check the tiles' URLs, so it happens only on this page.
  let { live }: { live: Live } = $props();

  let tiles = $state.raw<Tile[] | null>(null);
  let status = $state.raw<Record<string, Status>>({});
  let containers = $state.raw<Container[]>([]);
  let editing = $state(false);
  let formFor = $state<{ form: Form; id: number | null; iconData?: string; from?: Suggestion } | null>(null);
  // Suggestions (ROADMAP 2.10): looked for only when the user asks, since
  // it probes local ports; null while not shown.
  let suggestions = $state.raw<Suggestion[] | null>(null);
  let suggesting = $state(false);

  async function load(): Promise<void> {
    try {
      const r = await api<{ tiles: Tile[]; status: Record<string, Status> }>('/homepage');
      tiles = r.tiles;
      status = { ...status, ...r.status };
    } catch (err) {
      if (tiles === null) tiles = [];
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  $effect(() => {
    void load();
  });

  $effect(() =>
    live.subscribe('homepage', (data) => {
      const m = data as HomepageMsg;
      if (m?.kind === 'status' && m.status) status = m.status;
    }),
  );

  // Linked containers: one snapshot when tiles need it, not a live stream
  // (that would keep hearth-broker running whenever Home is open).
  const linked = $derived((tiles ?? []).some((x) => x.container !== ''));
  async function loadContainers(): Promise<void> {
    try {
      const s = await api<Snapshot>('/docker');
      containers = s.state === 'ok' ? s.containers : [];
    } catch {
      containers = [];
    }
  }
  $effect(() => {
    if (linked || formFor) void loadContainers();
  });

  const groups = $derived(groupTiles(tiles ?? []));
  const groupNames = $derived([...new Set((tiles ?? []).map((x) => x.group).filter(Boolean))]);
  const byName = $derived(new Map(containers.map((c) => [c.name, c])));

  async function save(f: Form): Promise<string | null> {
    const edit = formFor;
    if (!edit) return null;
    try {
      if (edit.id === null) await api('/homepage/tiles', { method: 'POST', body: JSON.stringify(f) });
      else await api(`/homepage/tiles/${edit.id}`, { method: 'PUT', body: JSON.stringify(f) });
      formFor = null;
      const from = edit.from;
      if (from && suggestions) suggestions = suggestions.filter((x) => x !== from);
      await load();
      return null;
    } catch (err) {
      return failure(err, 'action.failed');
    }
  }

  async function suggest(): Promise<void> {
    suggesting = true;
    try {
      suggestions = (await api<{ suggestions: Suggestion[] }>('/homepage/suggestions')).suggestions;
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      suggesting = false;
    }
  }

  const sourceText = (x: Suggestion): string =>
    x.container ? t('tile.fromContainer', { name: x.container }) : x.process ? t('tile.fromProcess', { name: x.process }) : t('tile.fromPort');

  async function remove(x: Tile): Promise<void> {
    try {
      await api(`/homepage/tiles/${x.id}`, { method: 'DELETE' });
      await load();
      toast(t('tile.deleted', { name: x.name }), {
        undo: () => {
          // An uploaded icon went with the tile; the copy has none.
          const f = { ...formOf(x), icon: x.icon.startsWith('upload:') ? '' : x.icon };
          void api('/homepage/tiles', { method: 'POST', body: JSON.stringify(f) }).then(load);
        },
      });
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function move(x: Tile, by: -1 | 1): Promise<void> {
    if (!tiles) return;
    try {
      await api('/homepage/order', { method: 'PUT', body: JSON.stringify({ ids: moved(tiles, x.id, by) }) });
      await load();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function containerAct(c: Container, op: 'start' | 'stop'): Promise<void> {
    toast(t('docker.authHint'));
    try {
      const res = await run('container', op, c.id);
      const bad = [...res.blocked, ...res.failed][0];
      if (res.done === 0 && bad) {
        const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
        toast(t('docker.failed', { op: t(op === 'start' ? 'docker.op.start' : 'docker.op.stop'), name: c.name, reason }), { tone: 'error' });
      } else {
        toast(t(op === 'start' ? 'docker.done.start' : 'docker.done.stop', { name: c.name }));
      }
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      await loadContainers();
    }
  }

  const stateText = (s: Status | undefined, port: number): string =>
    !s ? t('tile.unknown') : s.state === 'up' ? (port > 0 ? t('tile.portUp', { port, ms: s.latency_ms }) : t('tile.up', { ms: s.latency_ms })) : s.state === 'warn' ? t('tile.warn', { error: s.error ?? '' }) : t('tile.down', { error: s.error ?? '' });
</script>

<section aria-labelledby="tiles-title">
  <header>
    <h2 id="tiles-title">{t('tiles.title')}</h2>
    <span class="spacer"></span>
    {#if (tiles ?? []).length > 0}
      <button type="button" class="plain" aria-pressed={editing} onclick={() => (editing = !editing)}>{editing ? t('tiles.done') : t('tiles.edit')}</button>
    {/if}
    {#if suggestions === null}
      <button type="button" class="plain" title={t('tiles.suggestHint')} disabled={suggesting} onclick={suggest}>{suggesting ? t('tiles.suggesting') : t('tiles.suggest')}</button>
    {:else}
      <button type="button" class="plain" onclick={() => (suggestions = null)}>{t('tiles.suggestHide')}</button>
    {/if}
    <button type="button" class="plain" onclick={() => (formFor = { form: emptyForm(), id: null })}>{t('tiles.add')}</button>
  </header>

  {#if tiles !== null && tiles.length === 0}
    <EmptyState inline icon="globe" body={t('tiles.empty')} />
  {/if}

  {#each groups as g (g.group)}
    {#if g.group}<h3>{g.group}</h3>{/if}
    <ul class="tiles">
      {#each g.tiles as x (x.id)}
        {@const s = x.health_check ? status[String(x.id)] : undefined}
        {@const c = x.container ? byName.get(x.container) : undefined}
        {@const icon = builtinName(x.icon)}
        <li class="tile">
          <a href={x.url} target="_blank" rel="noopener noreferrer" class="open" title={x.url}>
            <span class="icon" aria-hidden="true">
              {#if x.icon_data}<img src={x.icon_data} alt="" width="32" height="32" />
              {:else if icon}<Icon name={icon} size={26} />
              {:else}<span class="letter">{(x.name[0] ?? '?').toUpperCase()}</span>{/if}
            </span>
            <span class="text">
              <span class="name">{x.name}</span>
              <span class="desc">{x.description || hostOf(x.url)}</span>
            </span>
          </a>
          <div class="foot">
            {#if x.health_check}
              <span class="dot {s?.state ?? 'unknown'}" aria-hidden="true"></span><span class="muted" role="status">{stateText(s, x.check_port)}</span>
            {/if}
            {#if c}
              <span class="spacer"></span>
              <span class="muted" title={c.status}>{c.name}</span>
              {#if c.state === 'running'}
                <button type="button" class="mini" onclick={() => containerAct(c, 'stop')}>{t('docker.op.stop')}</button>
              {:else if c.state !== 'removing'}
                <button type="button" class="mini" onclick={() => containerAct(c, 'start')}>{t('docker.op.start')}</button>
              {/if}
            {/if}
          </div>
          {#if s?.cert_expires}
            <p class="cert muted">{t('tile.certUntil', { date: new Date(s.cert_expires).toLocaleDateString(locale()) })}</p>
          {/if}
          {#if editing}
            <div class="edit">
              <button type="button" class="mini" aria-label={t('tile.earlier', { name: x.name })} onclick={() => move(x, -1)}>{t('arrow.earlier')}</button>
              <button type="button" class="mini" aria-label={t('tile.later', { name: x.name })} onclick={() => move(x, 1)}>{t('arrow.later')}</button>
              <button type="button" class="mini" onclick={() => (formFor = { form: formOf(x), id: x.id, ...(x.icon_data ? { iconData: x.icon_data } : {}) })}>{t('tile.editOne')}</button>
              <button type="button" class="mini danger" onclick={() => remove(x)}>{t('tile.delete')}</button>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/each}

  {#if suggestions !== null}
    {#if suggestions.length === 0}
      <p class="note">{t('tiles.suggestNone')}</p>
    {:else}
      <ul class="tiles suggested">
        {#each suggestions as x (x.url)}
          <li class="tile ghost">
            <button type="button" class="open" title={x.url} onclick={() => (formFor = { form: suggestionForm(x), id: null, from: x })}>
              <span class="icon" aria-hidden="true"><span class="letter">{(x.name[0] ?? '?').toUpperCase()}</span></span>
              <span class="text">
                <span class="name">{x.name}</span>
                <span class="desc">{hostOf(x.url)} · {sourceText(x)}</span>
              </span>
            </button>
            <div class="foot"><span class="muted">{t('tile.suggestAdd')}</span></div>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</section>

<TileForm
  form={formFor?.form ?? null}
  title={formFor?.id === null ? t('tile.addTitle') : t('tile.editTitle')}
  groups={groupNames}
  containers={containers.map((c) => c.name).sort()}
  {...formFor?.iconData ? { iconData: formFor.iconData } : {}}
  onsave={save}
  oncancel={() => (formFor = null)}
/>

<style>
  section {
    margin-bottom: var(--space-6);
  }
  header {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-3);
  }
  h2 {
    font-size: 16px;
  }
  h3 {
    font-size: 13px;
    color: var(--text-muted);
    font-weight: 600;
    margin: var(--space-3) 0 var(--space-2);
  }
  .spacer {
    flex: 1;
  }
  .note,
  .muted {
    color: var(--text-muted);
  }
  .note {
    font-size: 13px;
  }
  .tiles {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: var(--space-3);
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    padding: var(--space-3);
    font-size: 13px;
  }
  .open {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    color: var(--text);
    text-decoration: none;
    min-width: 0;
  }
  .suggested {
    margin-top: var(--space-3);
  }
  .ghost {
    background: transparent;
    border-style: dashed;
    box-shadow: none;
    color: var(--text-muted);
  }
  button.open {
    font: inherit;
    background: none;
    border: 0;
    padding: 0;
    text-align: start;
    cursor: pointer;
    color: var(--text-muted);
  }
  .ghost .icon {
    color: var(--text-muted);
  }
  .open:hover .name,
  .open:focus-visible .name {
    text-decoration: underline;
  }
  .icon {
    display: grid;
    place-items: center;
    width: 40px;
    height: 40px;
    flex-shrink: 0;
    border-radius: var(--radius-s);
    background: var(--surface-2);
    color: var(--accent);
  }
  .icon img {
    object-fit: contain;
  }
  .letter {
    font-weight: 700;
    font-size: 18px;
  }
  .text {
    display: grid;
    min-width: 0;
  }
  .name {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .desc {
    color: var(--text-muted);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .foot,
  .edit {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-size: 12px;
    min-height: 20px;
  }
  .edit {
    border-top: 1px solid var(--border);
    padding-top: var(--space-2);
  }
  .cert {
    font-size: 12px;
    margin: 0;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--border);
    flex-shrink: 0;
  }
  .dot.up {
    background: var(--ok);
  }
  .dot.warn {
    background: var(--warn);
  }
  .dot.down {
    background: var(--crit);
  }
  .plain,
  .mini {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    cursor: pointer;
  }
  .plain {
    font-size: 13px;
    padding: var(--space-1) var(--space-3);
  }
  .mini {
    font-size: 12px;
    padding: 0 var(--space-2);
  }
  .danger {
    color: var(--crit);
  }
</style>

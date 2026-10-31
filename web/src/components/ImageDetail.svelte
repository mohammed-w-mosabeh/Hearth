<script lang="ts">
  import { api, ApiError } from '../lib/api';
  import {
    envPairs,
    imageTitle,
    layerCommand,
    SEVERITIES,
    shortID,
    uniqueSize,
    vulnURL,
    type Container,
    type Image,
    type Layer,
    type Scan,
    type TagUpdate,
  } from '../lib/docker';
  import { bytes } from '../lib/format';
  import { locale, type MessageKey, t } from '../lib/i18n';

  // One image: overview from a masked inspect, the layer history, the
  // registry update check and the optional Trivy scan. The last two run
  // only when asked. Everything Docker, registries or Trivy report is text.
  let {
    image,
    containers,
    initialTab = 'overview',
    onpull,
    onclose,
  }: {
    image: Image | null;
    containers: Container[];
    initialTab?: Tab;
    onpull: (ref: string) => void;
    onclose: () => void;
  } = $props();

  type Tab = 'overview' | 'layers' | 'updates' | 'scan';
  const tabs: { id: Tab; label: MessageKey }[] = [
    { id: 'overview', label: 'image.tab.overview' },
    { id: 'layers', label: 'image.tab.layers' },
    { id: 'updates', label: 'image.tab.updates' },
    { id: 'scan', label: 'image.tab.scan' },
  ];

  interface Inspect {
    Id: string;
    Created?: string;
    Architecture?: string;
    Variant?: string;
    Os?: string;
    Author?: string;
    Config?: {
      User?: string;
      ExposedPorts?: Record<string, unknown> | null;
      Env?: string[] | null;
      Entrypoint?: string[] | null;
      Cmd?: string[] | null;
      WorkingDir?: string;
      Labels?: Record<string, string> | null;
    };
    RootFS?: { Layers?: string[] };
  }

  let dialog: HTMLDialogElement;
  let tab = $state<Tab>('overview');
  let inspect = $state.raw<Inspect | null>(null);
  let layers = $state.raw<Layer[]>([]);
  let failed = $state('');
  let updates = $state.raw<TagUpdate[] | null>(null);
  let checking = $state(false);
  let trivy = $state<{ installed: boolean; version?: string } | null>(null);
  let scan = $state.raw<Scan | null>(null);
  let scanning = $state(false);
  let scanError = $state('');

  const id = $derived(image?.id ?? '');
  const path = $derived(`/docker/images/${encodeURIComponent(id)}`);

  $effect(() => {
    if (!image) {
      if (dialog.open) dialog.close();
      return;
    }
    if (!dialog.open) dialog.showModal();
  });

  // Reset and load when another image is shown.
  $effect(() => {
    if (!id) return;
    tab = initialTab;
    inspect = null;
    layers = [];
    failed = '';
    updates = null;
    scan = null;
    scanError = '';
    let stopped = false;
    api<{ inspect: Inspect; history: Layer[] }>(path)
      .then((d) => {
        if (stopped) return;
        inspect = d.inspect;
        layers = Array.isArray(d.history) ? d.history : [];
      })
      .catch((err: unknown) => {
        if (!stopped) failed = err instanceof ApiError ? err.message : t('action.failed');
      });
    return () => {
      stopped = true;
    };
  });

  $effect(() => {
    if (tab !== 'scan' || trivy) return;
    api<{ installed: boolean; version?: string }>('/docker/trivy')
      .then((r) => (trivy = r))
      .catch(() => (trivy = { installed: false }));
  });

  async function checkUpdates(): Promise<void> {
    checking = true;
    try {
      updates = (await api<{ tags: TagUpdate[] }>(`${path}/updates`)).tags;
    } catch (err) {
      failed = err instanceof ApiError ? err.message : t('action.failed');
    } finally {
      checking = false;
    }
  }

  async function runScan(): Promise<void> {
    scanning = true;
    scanError = '';
    try {
      scan = await api<Scan>(`${path}/scan`, { method: 'POST' });
    } catch (err) {
      scanError = err instanceof ApiError ? err.message : t('action.failed');
    } finally {
      scanning = false;
    }
  }

  const users = $derived(image ? containers.filter((c) => image.containers.includes(c.id)) : []);
  const words = (a: string[] | null | undefined): string => (a && a.length > 0 ? a.join(' ') : '–');
  const when = (s: number | string | undefined): string => {
    if (s === undefined || s === 0) return '–';
    const d = typeof s === 'number' ? new Date(s * 1000) : new Date(s);
    return Number.isNaN(d.getTime()) ? String(s) : d.toLocaleString(locale());
  };
  const updateKey: Record<TagUpdate['status'], MessageKey> = {
    current: 'image.update.current',
    update: 'image.update.update',
    local: 'image.update.local',
    unknown: 'image.update.unknown',
  };
</script>

<dialog bind:this={dialog} aria-labelledby="image-title" onclose={() => image && onclose()}>
  {#if image}
    {@const im = image}
    <header>
      <h2 id="image-title">{imageTitle(im)}</h2>
      <span class="muted mono">{shortID(im.id)}</span>
      <button type="button" class="x" aria-label={t('detail.close')} onclick={() => onclose()}>×</button>
    </header>
    <div class="tabs" role="tablist" aria-label={t('detail.tab')}>
      {#each tabs as tb (tb.id)}
        <button type="button" role="tab" aria-selected={tab === tb.id} class:on={tab === tb.id} onclick={() => (tab = tb.id)}>
          {t(tb.label)}
        </button>
      {/each}
    </div>
    {#if failed}<p class="note error" role="status">{failed}</p>{/if}

    <div class="body" role="tabpanel">
      {#if tab === 'overview'}
        {#if !inspect}
          <p class="note">{t('detail.loading')}</p>
        {:else}
          {@const cfg = inspect.Config ?? {}}
          <dl>
            <dt>{t('image.tags')}</dt>
            <dd class="mono">{im.tags.length > 0 ? im.tags.join(', ') : t('image.untagged')}</dd>
            <dt>{t('image.digests')}</dt>
            <dd class="mono wrap">{im.digests.length > 0 ? im.digests.join(', ') : '–'}</dd>
            <dt>{t('detail.id')}</dt>
            <dd class="mono wrap">{im.id}</dd>
            <dt>{t('detail.created')}</dt>
            <dd>{when(inspect.Created ?? im.created)}</dd>
            <dt>{t('image.size')}</dt>
            <dd>
              {bytes(im.size)}{#if uniqueSize(im) !== null}
                · {t('image.sizeSplit', { unique: bytes(uniqueSize(im) ?? 0), shared: bytes(im.shared_size) })}{/if}
            </dd>
            <dt>{t('image.platform')}</dt>
            <dd>{inspect.Os ?? '?'}/{inspect.Architecture ?? '?'}{inspect.Variant ? `/${inspect.Variant}` : ''}</dd>
            <dt>{t('detail.user')}</dt>
            <dd class="mono">{cfg.User || 'root'}</dd>
            <dt>{t('image.ports')}</dt>
            <dd class="mono">{Object.keys(cfg.ExposedPorts ?? {}).join(', ') || '–'}</dd>
            <dt>{t('detail.entrypoint')}</dt>
            <dd class="mono">{words(cfg.Entrypoint)}</dd>
            <dt>{t('detail.command')}</dt>
            <dd class="mono">{words(cfg.Cmd)}</dd>
            {#if cfg.WorkingDir}
              <dt>{t('detail.workdir')}</dt>
              <dd class="mono">{cfg.WorkingDir}</dd>
            {/if}
            <dt>{t('image.layerCount')}</dt>
            <dd>{inspect.RootFS?.Layers?.length ?? '–'}</dd>
          </dl>

          <h3>{t('image.usedBy')}</h3>
          {#if users.length === 0}<p class="note">{t('image.unused')}</p>{:else}
            <ul class="chips">{#each users as c (c.id)}<li>{c.name} <span class="muted">{c.state}</span></li>{/each}</ul>
          {/if}

          <h3>{t('detail.env')}</h3>
          <p class="note small">{t('detail.envMasked')}</p>
          {#if envPairs(cfg.Env).length === 0}<p class="note">{t('detail.none')}</p>{:else}
            <table>
              <tbody>
                {#each envPairs(cfg.Env) as [k, v], i (i)}<tr><th class="mono">{k}</th><td class="mono wrap">{v}</td></tr>{/each}
              </tbody>
            </table>
          {/if}

          <h3>{t('detail.labels')}</h3>
          {#if Object.keys(cfg.Labels ?? {}).length === 0}<p class="note">{t('detail.none')}</p>{:else}
            <table>
              <tbody>
                {#each Object.entries(cfg.Labels ?? {}).sort(([a], [b]) => (a < b ? -1 : 1)) as [k, v] (k)}
                  <tr><th class="mono">{k}</th><td class="mono wrap">{v}</td></tr>
                {/each}
              </tbody>
            </table>
          {/if}
        {/if}
      {:else if tab === 'layers'}
        {#if layers.length === 0}
          <p class="note">{failed ? '' : t('detail.loading')}</p>
        {:else}
          <p class="note small">{t('image.layersIntro', { n: layers.length })}</p>
          <table class="layers">
            <thead>
              <tr><th>{t('image.layer.created')}</th><th class="num">{t('image.layer.size')}</th><th>{t('image.layer.command')}</th></tr>
            </thead>
            <tbody>
              {#each layers as l, i (i)}
                <tr class:empty={l.Size === 0}>
                  <td class="nowrap">{when(l.Created)}</td>
                  <td class="num nowrap">{bytes(l.Size)}</td>
                  <td class="mono wrap">{layerCommand(l.CreatedBy) || '–'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if tab === 'updates'}
        <p class="note small">{t('image.update.intro')}</p>
        {#if im.tags.length === 0}
          <p class="note">{t('image.update.untagged')}</p>
        {:else}
          <div class="bar">
            <button type="button" class="plain" disabled={checking} onclick={checkUpdates}>
              {checking ? t('image.update.checking') : t('image.update.check')}
            </button>
          </div>
          {#if updates}
            <table>
              <tbody>
                {#each updates as u (u.tag)}
                  <tr>
                    <th class="mono">{u.tag}</th>
                    <td class:upd={u.status === 'update'}>{t(updateKey[u.status])}{u.error ? ` — ${u.error}` : ''}</td>
                    <td>
                      {#if u.status === 'update'}
                        <button type="button" class="plain" onclick={() => onpull(u.tag)}>{t('image.op.pull')}</button>
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        {/if}
      {:else if tab === 'scan'}
        {#if !trivy}
          <p class="note">{t('detail.loading')}</p>
        {:else if !trivy.installed}
          <p class="note">{t('image.scan.missing')}</p>
        {:else}
          <p class="note small">{t('image.scan.intro', { version: trivy.version ?? '' })}</p>
          <div class="bar">
            <button type="button" class="plain" disabled={scanning} onclick={runScan}>
              {scanning ? t('image.scan.running') : t('image.scan.run')}
            </button>
          </div>
          {#if scanError}<p class="note error" role="status">{scanError}</p>{/if}
          {#if scan}
            <p class="counts">
              {#each SEVERITIES as sev (sev)}
                <span class="sev {sev.toLowerCase()}">{sev} {scan.counts[sev] ?? 0}</span>
              {/each}
            </p>
            {#if scan.vulns.length === 0}
              <p class="note">{t('image.scan.clean')}</p>
            {:else}
              <table>
                <thead>
                  <tr>
                    <th>{t('image.scan.id')}</th><th>{t('image.scan.package')}</th><th>{t('image.scan.installed')}</th><th>{t('image.scan.fixed')}</th><th>{t('image.scan.severity')}</th>
                  </tr>
                </thead>
                <tbody>
                  {#each scan.vulns as v, i (i)}
                    {@const url = vulnURL(v.id)}
                    <tr>
                      <td class="mono nowrap">{#if url}<a href={url} target="_blank" rel="noopener noreferrer">{v.id}</a>{:else}{v.id}{/if}</td>
                      <td class="mono">{v.package}</td>
                      <td class="mono">{v.installed}</td>
                      <td class="mono">{v.fixed || '–'}</td>
                      <td><span class="sev {v.severity.toLowerCase()}">{v.severity}</span></td>
                    </tr>
                  {/each}
                </tbody>
              </table>
              {#if scan.truncated}<p class="note small">{t('image.scan.truncated')}</p>{/if}
            {/if}
          {/if}
        {/if}
      {/if}
    </div>
  {/if}
</dialog>

<style>
  dialog {
    width: min(1000px, calc(100vw - 32px));
    height: min(760px, calc(100vh - 32px));
    padding: var(--space-4);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog[open] {
    display: flex;
    flex-direction: column;
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  header {
    display: flex;
    align-items: baseline;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
  }
  h2 {
    font-size: 17px;
    overflow-wrap: anywhere;
  }
  header .muted {
    flex: 1;
  }
  .x {
    font: inherit;
    font-size: 20px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 0;
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
  }
  .tabs {
    display: flex;
    gap: var(--space-1);
    border-bottom: 1px solid var(--border);
    margin-bottom: var(--space-3);
  }
  .tabs button {
    font: inherit;
    font-size: 13px;
    color: var(--text-muted);
    background: none;
    border: 0;
    border-bottom: 2px solid transparent;
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .tabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
  h3 {
    font-size: 13px;
    font-weight: 600;
    margin: var(--space-4) 0 var(--space-2);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--space-1) var(--space-4);
    margin: 0;
    font-size: 13px;
  }
  dt {
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  table {
    border-collapse: collapse;
    font-size: 12px;
    width: 100%;
  }
  th,
  td {
    text-align: start;
    vertical-align: top;
    padding: 3px var(--space-3) 3px 0;
    border-bottom: 1px solid var(--border);
  }
  th {
    font-weight: 500;
    white-space: nowrap;
  }
  thead th {
    color: var(--text-muted);
    font-weight: 600;
  }
  .num {
    text-align: end;
  }
  .nowrap {
    white-space: nowrap;
  }
  .layers tr.empty td {
    color: var(--text-muted);
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .wrap {
    overflow-wrap: anywhere;
  }
  .chips {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    font-size: 13px;
  }
  .chips li {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 var(--space-2);
  }
  .note,
  .muted {
    color: var(--text-muted);
    font-size: 13px;
  }
  .note {
    margin: var(--space-2) 0;
  }
  .small {
    font-size: 12px;
  }
  .error {
    color: var(--crit);
  }
  .upd {
    color: var(--warn);
    font-weight: 500;
  }
  .bar {
    margin-bottom: var(--space-3);
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
  }
  .plain:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .counts {
    display: flex;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
  .sev {
    font-size: 11px;
    font-weight: 600;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
  }
  .sev.critical,
  .sev.high {
    color: var(--crit);
    border-color: var(--crit);
  }
  .sev.medium {
    color: var(--warn);
    border-color: var(--warn);
  }
  a {
    color: var(--accent);
  }
</style>

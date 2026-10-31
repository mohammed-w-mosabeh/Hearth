<script lang="ts">
  import ContainerShell from './ContainerShell.svelte';
  import Sparkline from './Sparkline.svelte';
  import { api, ApiError } from '../lib/api';
  import {
    appendLogs,
    columns,
    envPairs,
    filterLogs,
    portText,
    ports,
    STATS_WINDOW_S,
    type Container,
    type LogLine,
    type LogsMsg,
    type Point,
  } from '../lib/docker';
  import { bytes, pct, rate } from '../lib/format';
  import { locale, type MessageKey, t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  // One container: overview (from a masked inspect), usage history, live
  // logs and the raw inspect. Everything Docker reports is shown as text.
  let {
    container,
    history,
    live,
    onclose,
  }: { container: Container | null; history: Point[]; live: Live; onclose: () => void } = $props();

  type Tab = 'overview' | 'stats' | 'logs' | 'shell' | 'inspect';
  const allTabs: { id: Tab; label: MessageKey }[] = [
    { id: 'overview', label: 'detail.tab.overview' },
    { id: 'stats', label: 'detail.tab.stats' },
    { id: 'logs', label: 'detail.tab.logs' },
    { id: 'shell', label: 'detail.tab.shell' },
    { id: 'inspect', label: 'detail.tab.inspect' },
  ];
  // A shell needs a running container.
  const tabs = $derived(allTabs.filter((tb) => tb.id !== 'shell' || container?.state === 'running'));

  interface Inspect {
    Id: string;
    Name: string;
    Created: string;
    Path?: string;
    Args?: string[];
    RestartCount?: number;
    State?: { Status?: string; StartedAt?: string; FinishedAt?: string; ExitCode?: number; Health?: { Status?: string } };
    Config?: {
      Image?: string;
      Cmd?: string[] | null;
      Entrypoint?: string[] | null;
      Env?: string[] | null;
      Labels?: Record<string, string> | null;
      WorkingDir?: string;
      User?: string;
    };
    HostConfig?: { RestartPolicy?: { Name?: string; MaximumRetryCount?: number } };
    NetworkSettings?: { Networks?: Record<string, { IPAddress?: string; Gateway?: string }> | null };
    Mounts?: { Type?: string; Name?: string; Source?: string; Destination?: string; RW?: boolean }[] | null;
  }
  interface InspectResult {
    inspect: Inspect;
    risks: string[];
  }

  let dialog: HTMLDialogElement;
  let tab = $state<Tab>('overview');
  let info = $state.raw<InspectResult | null>(null);
  let failed = $state('');

  const id = $derived(container?.id ?? '');
  // Inspect again when the container changes state (started, stopped...).
  const stamp = $derived(container ? `${container.id}|${container.state}|${container.status.split(' ')[0]}` : '');

  $effect(() => {
    if (!container) {
      if (dialog.open) dialog.close();
      return;
    }
    if (!dialog.open) {
      tab = 'overview';
      dialog.showModal();
    }
  });

  $effect(() => {
    if (!stamp) return;
    const want = id;
    let stopped = false;
    failed = '';
    api<InspectResult>(`/docker/containers/${encodeURIComponent(want)}`)
      .then((r) => {
        if (!stopped) info = r;
      })
      .catch((err: unknown) => {
        if (!stopped) failed = err instanceof ApiError ? err.message : t('action.failed');
      });
    return () => {
      stopped = true;
    };
  });

  $effect(() => {
    if (!container) info = null;
    else if (tab === 'shell' && container.state !== 'running') tab = 'overview';
  });

  // Live logs while the Logs tab is open.
  let lines = $state.raw<LogLine[]>([]);
  let ended = $state<{ error?: string } | null>(null);
  let filter = $state('');
  let stderrOnly = $state(false);
  let follow = $state(true);
  let list = $state<HTMLElement | null>(null);

  $effect(() => {
    if (tab !== 'logs' || !id) return;
    lines = [];
    ended = null;
    follow = true;
    return live.subscribe(`docker.logs:${id}`, (data) => {
      const m = data as LogsMsg;
      if (m?.kind !== 'lines' && m?.kind !== 'end') return;
      lines = appendLogs(lines, m);
      ended = m.kind === 'end' ? { ...(m.error ? { error: m.error } : {}) } : m.reset ? null : ended;
    });
  });

  const shown = $derived(filterLogs(lines, filter, stderrOnly));

  $effect(() => {
    void shown.length;
    if (follow && list) list.scrollTop = list.scrollHeight;
  });

  function onscroll(): void {
    if (list) follow = list.scrollHeight - list.scrollTop - list.clientHeight < 24;
  }

  const last = $derived(history.at(-1));
  const cpuCols = $derived(columns(history, 'cpu'));
  const memCols = $derived(columns(history, 'mem'));
  const netCols = $derived(columns(history, 'net_rx', 'net_tx'));
  const blkCols = $derived(columns(history, 'blk_read', 'blk_write'));

  function riskText(r: string): string {
    if (r.startsWith('host-mount:')) return t('detail.risk.host-mount', { path: r.slice('host-mount:'.length) });
    const key = `detail.risk.${r}` as MessageKey;
    const text = t(key);
    return text === key ? r : text;
  }

  function when(iso: string | undefined): string {
    if (!iso || iso.startsWith('0001-')) return '–';
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(locale());
  }

  const words = (a: string[] | null | undefined): string => (a && a.length > 0 ? a.join(' ') : '–');

  async function copy(): Promise<void> {
    if (!info) return;
    try {
      await navigator.clipboard.writeText(JSON.stringify(info.inspect, null, 2));
      toast(t('detail.inspect.copied'));
    } catch {
      toast(t('action.failed'), { tone: 'error' });
    }
  }
</script>

<dialog bind:this={dialog} aria-labelledby="detail-title" onclose={() => container && onclose()}>
  {#if container}
    {@const c = container}
    {@const ins = info?.inspect}
    <header>
      <h2 id="detail-title">{c.name}</h2>
      <span class="muted">{c.image}</span>
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
        {#if !ins}
          <p class="note">{t('detail.loading')}</p>
        {:else}
          {#if info && info.risks.length > 0}
            <div class="risks" role="note">
              <strong>{t('detail.risks')}</strong>
              <span>{t('detail.risksBody')}</span>
              <ul>
                {#each info.risks as r (r)}<li>{riskText(r)}</li>{/each}
              </ul>
            </div>
          {/if}
          <dl>
            <dt>{t('detail.state')}</dt>
            <dd>{c.status}{c.health ? ` · ${c.health}` : ''}</dd>
            <dt>{t('detail.started')}</dt>
            <dd>{when(ins.State?.StartedAt)}</dd>
            {#if c.state === 'exited'}
              <dt>{t('detail.finished')}</dt>
              <dd>{when(ins.State?.FinishedAt)} · {t('detail.exitCode')} {ins.State?.ExitCode ?? '–'}</dd>
            {/if}
            <dt>{t('detail.restartPolicy')}</dt>
            <dd>
              {ins.HostConfig?.RestartPolicy?.Name || 'no'}{ins.HostConfig?.RestartPolicy?.MaximumRetryCount
                ? ` (${ins.HostConfig.RestartPolicy.MaximumRetryCount})`
                : ''} · {t('detail.restarts')} {ins.RestartCount ?? 0}
            </dd>
            <dt>{t('detail.image')}</dt>
            <dd class="mono">{ins.Config?.Image ?? c.image}</dd>
            <dt>{t('detail.entrypoint')}</dt>
            <dd class="mono">{words(ins.Config?.Entrypoint)}</dd>
            <dt>{t('detail.command')}</dt>
            <dd class="mono">{words(ins.Config?.Cmd)}</dd>
            {#if ins.Config?.WorkingDir}
              <dt>{t('detail.workdir')}</dt>
              <dd class="mono">{ins.Config.WorkingDir}</dd>
            {/if}
            <dt>{t('detail.user')}</dt>
            <dd class="mono">{ins.Config?.User || 'root'}</dd>
            {#if c.project}
              <dt>{t('detail.compose')}</dt>
              <dd>{c.project}{c.service ? ` / ${c.service}` : ''}</dd>
            {/if}
            <dt>{t('detail.created')}</dt>
            <dd>{when(ins.Created)}</dd>
            <dt>{t('detail.id')}</dt>
            <dd class="mono">{c.id}</dd>
          </dl>

          <h3>{t('detail.ports')}</h3>
          {#if c.ports.length === 0}<p class="note">{t('detail.none')}</p>{:else}
            <ul class="chips">{#each ports(c) as p (portText(p))}<li class="mono">{portText(p)}</li>{/each}</ul>
          {/if}

          <h3>{t('detail.networks')}</h3>
          {#if Object.keys(ins.NetworkSettings?.Networks ?? {}).length === 0}<p class="note">{t('detail.none')}</p>{:else}
            <table>
              <tbody>
                {#each Object.entries(ins.NetworkSettings?.Networks ?? {}) as [name, n] (name)}
                  <tr><th>{name}</th><td class="mono">{n.IPAddress || '–'}</td></tr>
                {/each}
              </tbody>
            </table>
          {/if}

          <h3>{t('detail.mounts')}</h3>
          {#if (ins.Mounts ?? []).length === 0}<p class="note">{t('detail.none')}</p>{:else}
            <table>
              <tbody>
                {#each ins.Mounts ?? [] as m, i (i)}
                  <tr>
                    <th class="mono">{m.Destination}</th>
                    <td class="mono">{m.Type === 'volume' ? m.Name : m.Source}</td>
                    <td class="muted">{m.Type}{m.RW === false ? ` · ${t('detail.mountRO')}` : ''}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}

          <h3>{t('detail.env')}</h3>
          <p class="note small">{t('detail.envMasked')}</p>
          {#if envPairs(ins.Config?.Env).length === 0}<p class="note">{t('detail.none')}</p>{:else}
            <table>
              <tbody>
                {#each envPairs(ins.Config?.Env) as [k, v], i (i)}
                  <tr><th class="mono">{k}</th><td class="mono wrap">{v}</td></tr>
                {/each}
              </tbody>
            </table>
          {/if}

          <h3>{t('detail.labels')}</h3>
          {#if Object.keys(ins.Config?.Labels ?? {}).length === 0}<p class="note">{t('detail.none')}</p>{:else}
            <table>
              <tbody>
                {#each Object.entries(ins.Config?.Labels ?? {}).sort(([a], [b]) => (a < b ? -1 : 1)) as [k, v] (k)}
                  <tr><th class="mono">{k}</th><td class="mono wrap">{v}</td></tr>
                {/each}
              </tbody>
            </table>
          {/if}
        {/if}
      {:else if tab === 'stats'}
        {#if !last}
          <p class="note">{t('detail.stats.none')}</p>
        {:else}
          <div class="charts">
            <section>
              <h3>{t('detail.stats.cpu')} <span class="val">{pct(last.cpu, 1)}</span></h3>
              <Sparkline data={cpuCols} span={STATS_WINDOW_S} height={64} label={t('detail.stats.cpu')} />
            </section>
            <section>
              <h3>
                {t('detail.stats.mem')}
                <span class="val">{last.mem_limit ? t('detail.stats.memOf', { used: bytes(last.mem), limit: bytes(last.mem_limit) }) : bytes(last.mem)}</span>
              </h3>
              <Sparkline data={memCols} span={STATS_WINDOW_S} height={64} label={t('detail.stats.mem')} />
            </section>
            <section>
              <h3>{t('detail.stats.net')} <span class="val">{rate(last.net_rx)} / {rate(last.net_tx)}</span></h3>
              <Sparkline data={netCols} colors={['--series-1', '--series-2']} span={STATS_WINDOW_S} height={64} label={t('detail.stats.net')} />
            </section>
            <section>
              <h3>{t('detail.stats.blk')} <span class="val">{rate(last.blk_read)} / {rate(last.blk_write)}</span></h3>
              <Sparkline data={blkCols} colors={['--series-1', '--series-2']} span={STATS_WINDOW_S} height={64} label={t('detail.stats.blk')} />
            </section>
          </div>
          <p class="note small">{t('detail.stats.pids')}: {last.pids}</p>
        {/if}
      {:else if tab === 'logs'}
        <div class="bar">
          <input type="search" placeholder={t('detail.logs.filter')} aria-label={t('detail.logs.filter')} bind:value={filter} />
          <label><input type="checkbox" bind:checked={stderrOnly} /> {t('detail.logs.stderr')}</label>
        </div>
        <ol class="logs" bind:this={list} {onscroll}>
          {#each shown as l, i (i)}
            <li class:err={l.stream === 'stderr'}>{l.text}</li>
          {/each}
        </ol>
        {#if lines.length === 0 && !ended}<p class="note">{t('detail.logs.waiting')}</p>{/if}
        {#if lines.length > 0 && shown.length === 0}<p class="note">{t('detail.logs.noMatch')}</p>{/if}
        {#if ended}
          <p class="note" role="status">{ended.error ? t('detail.logs.endedError', { error: ended.error }) : t('detail.logs.ended')}</p>
        {/if}
        <p class="note small">{t('detail.logs.foot', { n: lines.length })}</p>
      {:else if tab === 'shell'}
        {#if !info}
          <p class="note">{t('detail.loading')}</p>
        {:else}
          <ContainerShell id={c.id} name={c.name} refused={info.risks.map(riskText)} />
        {/if}
      {:else if tab === 'inspect'}
        {#if !ins}
          <p class="note">{t('detail.loading')}</p>
        {:else}
          <div class="bar"><button type="button" class="plain" onclick={copy}>{t('detail.inspect.copy')}</button></div>
          <pre>{JSON.stringify(ins, null, 2)}</pre>
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
  }
  header .muted {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
    display: flex;
    flex-direction: column;
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
    padding: 2px var(--space-3) 2px 0;
    border-bottom: 1px solid var(--border);
  }
  th {
    font-weight: 500;
    white-space: nowrap;
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
  .risks {
    border-inline-start: 3px solid var(--warn);
    padding: var(--space-2) var(--space-3);
    margin-bottom: var(--space-3);
    font-size: 13px;
  }
  .risks strong {
    color: var(--warn);
    margin-inline-end: var(--space-2);
  }
  .risks ul {
    margin: var(--space-1) 0 0;
    padding-inline-start: var(--space-5);
  }
  .charts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: var(--space-4);
  }
  .charts h3 {
    display: flex;
    justify-content: space-between;
    margin-top: 0;
  }
  .val {
    font-weight: 400;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    align-items: center;
    margin-bottom: var(--space-2);
    font-size: 13px;
  }
  input[type='search'] {
    font: inherit;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    min-width: 260px;
  }
  .plain {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .logs,
  pre {
    flex: 1;
    min-height: 200px;
    overflow: auto;
    margin: 0;
    padding: var(--space-2);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
  }
  .logs {
    list-style: none;
  }
  .logs li {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .logs li.err {
    color: var(--crit);
  }
</style>

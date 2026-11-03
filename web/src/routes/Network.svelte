<script lang="ts">
  import Connectivity from '../components/Connectivity.svelte';
  import FirewallCard from '../components/FirewallCard.svelte';
  import LineageDialog from '../components/LineageDialog.svelte';
  import { api, ApiError } from '../lib/api';
  import { bytes, rate } from '../lib/format';
  import { t, type MessageKey } from '../lib/i18n';
  import {
    connKey,
    connRates,
    filterConnections,
    filterRows,
    hostPort,
    mainInterfaces,
    ownerName,
    primary,
    rows,
    vpns,
    type Endpoint,
    type NetState,
    type Row,
    type ScopeFilter,
    type Sockets,
  } from '../lib/network';
  import { openInspect, router } from '../lib/router.svelte';
  import { emberRequested } from '../lib/ember';

  // Network & ports (SPEC 11): who listens where, current connections and
  // their throughput, addressing, Wi-Fi/VPN and the firewall. Sockets are
  // read every few seconds while the page is visible.
  const SOCKETS_MS = 5000;
  const STATE_MS = 15000;

  // On Ember "What is this?" opens the inspector drawer (ROADMAP 8.7).
  const ember = emberRequested(location.search);

  let sockets = $state.raw<Sockets | null>(null);
  let info = $state.raw<NetState | null>(null);
  let failed = $state('');
  let rates = $state.raw(new Map<string, { up: number; down: number }>());
  let prev = new Map<string, Endpoint>();
  let prevAt = 0;

  async function loadSockets(): Promise<void> {
    try {
      const s = await api<Sockets>('/net/sockets');
      const now = Date.now();
      rates = connRates(prev, s.connections, prevAt ? (now - prevAt) / 1000 : 0);
      prev = new Map(s.connections.map((c) => [connKey(c), c]));
      prevAt = now;
      sockets = s;
      failed = '';
    } catch (err) {
      failed = err instanceof ApiError ? err.message : t('net.error');
    }
  }
  async function loadState(): Promise<void> {
    try {
      info = await api<NetState>('/net/state');
    } catch {
      // the summary stays as it was; sockets report errors
    }
  }
  $effect(() => {
    void loadSockets();
    void loadState();
    const a = setInterval(() => {
      if (document.visibilityState === 'visible') void loadSockets();
    }, SOCKETS_MS);
    const b = setInterval(() => {
      if (document.visibilityState === 'visible') void loadState();
    }, STATE_MS);
    return () => {
      clearInterval(a);
      clearInterval(b);
    };
  });

  type Tab = 'listening' | 'connections';
  let tab = $state<Tab>('listening');
  let search = $state('');
  let scope = $state<ScopeFilter>('all');
  let limit = $state(300);

  // Opened from elsewhere (#/network?q=8080).
  $effect(() => {
    if (router.q) {
      search = router.q;
      tab = 'listening';
      scope = 'all';
    }
  });

  const all = $derived(rows(sockets?.listeners ?? []));
  const shown = $derived(filterRows(all, scope, search));
  const conns = $derived(filterConnections(sockets?.connections ?? [], search));
  const exposed = $derived(all.filter((r) => r.scope !== 'loopback').length);
  $effect(() => {
    void [tab, search, scope];
    limit = 300;
  });

  const scopes: { id: ScopeFilter; label: MessageKey }[] = [
    { id: 'all', label: 'net.filter.all' },
    { id: 'exposed', label: 'net.filter.exposed' },
    { id: 'local', label: 'net.filter.local' },
  ];

  const main = $derived(info ? primary(info.nm) : null);
  const vpnList = $derived(info ? vpns(info.nm) : []);
  const ifaces = $derived(info ? mainInterfaces(info.interfaces) : []);
  const activeFw = $derived(info?.firewalls.filter((f) => f.active) ?? []);
  const installedFw = $derived(info?.firewalls.filter((f) => f.installed && !f.active) ?? []);

  let lineageOf = $state<Row | null>(null);
</script>

<header>
  <h1>{t('net.title')}</h1>
  <p>{sockets ? t('net.count', { n: all.length, exposed }) : t('net.intro')}</p>
</header>
{#if failed}<p class="note error" role="status">{failed}</p>{/if}

{#if info}
  <section class="cards" aria-label={t('net.summary')}>
    <div class="card">
      <h2>{t('net.card.connection')}</h2>
      {#if main}
        <p class="big">{main.ssid || main.id}</p>
        <p class="muted">
          {#if main.signal !== undefined}{t('net.signal', { n: main.signal })} · {/if}{main.devices.join(', ')}
        </p>
      {:else if info.nm.running}
        <p class="big">{t('net.noDefault')}</p>
      {:else}
        <p class="big">{ifaces[0]?.name ?? t('net.none')}</p>
        <p class="muted">{t('net.noNM')}</p>
      {/if}
      {#if info.nm.connectivity && info.nm.connectivity !== 'full'}
        <p class="warn">{t('net.connectivity', { state: info.nm.connectivity })}</p>
      {/if}
    </div>
    <div class="card">
      <h2>{t('net.card.addresses')}</h2>
      {#each ifaces.slice(0, 3) as i (i.name)}
        <p><span class="muted">{i.name}</span> <span class="mono">{i.addrs.filter((a) => !a.startsWith('fe80:')).slice(0, 2).join(', ') || i.addrs[0]}</span></p>
      {:else}
        <p class="muted">{t('net.none')}</p>
      {/each}
    </div>
    <div class="card">
      <h2>{t('net.card.route')}</h2>
      <p><span class="muted">{t('net.gateway')}</span> <span class="mono">{info.gateways.map((g) => g.gateway).join(', ') || '–'}</span></p>
      <p>
        <span class="muted">{t('net.dns')}</span> <span class="mono">{info.dns.slice(0, 3).join(', ') || '–'}</span>
        {#if info.dns_stub}<span class="muted" title={t('net.dnsStubHint')}> {t('net.dnsStub')}</span>{/if}
      </p>
    </div>
    <div class="card">
      <h2>{t('net.card.vpn')}</h2>
      {#if vpnList.length}
        {#each vpnList as v (v.id)}<p class="big">{v.id}</p>{/each}
      {:else}
        <p class="muted">{t('net.noVPN')}</p>
      {/if}
    </div>
    <div class="card">
      <h2>{t('net.card.firewall')}</h2>
      {#if activeFw.length}
        <p class="big ok">{activeFw.map((f) => f.name).join(', ')}</p>
        <p class="muted">{t('net.fwActive')}</p>
      {:else}
        <p class="big warn">{t('net.fwNone')}</p>
        <p class="muted">{installedFw.length ? t('net.fwInstalled', { names: installedFw.map((f) => f.name).join(', ') }) : t('net.fwHint')}</p>
      {/if}
    </div>
  </section>

  <FirewallCard />

  <details class="ifaces">
    <summary>{t('net.interfaces', { n: info.interfaces.length })}</summary>
    <div class="scroll">
      <table>
        <thead>
          <tr><th>{t('net.col.iface')}</th><th>{t('net.col.kind')}</th><th>{t('net.col.addrs')}</th><th>{t('net.col.mac')}</th></tr>
        </thead>
        <tbody>
          {#each info.interfaces as i (i.name)}
            <tr class:down={!i.up}>
              <td>{i.name}{#if !i.up}<span class="tag">{t('net.down')}</span>{/if}</td>
              <td>{i.kind}</td>
              <td class="mono wrap">{i.addrs.join(', ') || '–'}</td>
              <td class="mono">{i.mac || '–'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </details>
{/if}

<Connectivity />

<div class="bar">
  <div class="segmented" role="tablist" aria-label={t('net.view')}>
    <button type="button" role="tab" aria-selected={tab === 'listening'} class:on={tab === 'listening'} onclick={() => (tab = 'listening')}>
      {t('net.tab.listening')} <span class="n">{all.length}</span>
    </button>
    <button type="button" role="tab" aria-selected={tab === 'connections'} class:on={tab === 'connections'} onclick={() => (tab = 'connections')}>
      {t('net.tab.connections')} <span class="n">{sockets?.connections.length ?? 0}</span>
    </button>
  </div>
  <input type="search" placeholder={t('net.search')} aria-label={t('net.search')} bind:value={search} />
  {#if tab === 'listening'}
    <div class="segmented" role="radiogroup" aria-label={t('net.filter')}>
      {#each scopes as s (s.id)}
        <button type="button" role="radio" aria-checked={scope === s.id} class:on={scope === s.id} onclick={() => (scope = s.id)}>{t(s.label)}</button>
      {/each}
    </div>
  {/if}
</div>

{#if sockets === null && !failed}
  <p class="note">{t('net.loading')}</p>
{:else if tab === 'listening'}
  {#if shown.length === 0}
    <p class="note">{t('net.noneMatch')}</p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr>
            <th>{t('net.col.port')}</th>
            <th>{t('net.col.address')}</th>
            <th>{t('net.col.program')}</th>
            <th>{t('net.col.user')}</th>
            <th><span class="visually-hidden">{t('net.col.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each shown.slice(0, limit) as r (r.key)}
            <tr>
              <td class="port"><strong>{r.port}</strong> <span class="muted">{r.proto}</span></td>
              <td>
                <span class="mono">{r.addresses.join(', ')}</span>
                {#if r.scope === 'all'}
                  <span class="tag warn" title={t('net.scope.all')}>{t('net.tag.exposed')}</span>
                {:else if r.scope === 'address'}
                  <span class="tag" title={t('net.scope.address')}>{t('net.tag.network')}</span>
                {/if}
              </td>
              <td class="prog">
                {#if r.owner}
                  <span class="name">{ownerName(r.owner) || '?'}</span>
                  {#if r.owner.name && r.owner.name !== ownerName(r.owner)}<span class="muted">{r.owner.name}</span>{/if}
                  {#if r.owner.container}<span class="tag">{t('net.tag.container')}</span>{/if}
                {:else}
                  <span class="muted">{t('net.unknownOwner')}</span>
                {/if}
              </td>
              <td>{r.user}</td>
              <td class="act">
                <button type="button" class="what" onclick={() => (ember ? openInspect(`port:${r.proto}/${r.port}`) : (lineageOf = r))}>{t('net.whatIsThis')}</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if shown.length > limit}
      <button type="button" class="showmore" onclick={() => (limit += 300)}>{t('pkg.showMore', { n: shown.length - limit })}</button>
    {/if}
    <p class="foot">{t('net.footnote')}</p>
  {/if}
{:else if conns.length === 0}
  <p class="note">{t('net.noneMatch')}</p>
{:else}
  <div class="scroll">
    <table>
      <thead>
        <tr>
          <th>{t('net.col.local')}</th>
          <th>{t('net.col.remote')}</th>
          <th>{t('net.col.program')}</th>
          <th class="num">{t('net.col.rate')}</th>
          <th class="num">{t('net.col.total')}</th>
        </tr>
      </thead>
      <tbody>
        {#each conns.slice(0, limit) as c (connKey(c))}
          {@const r = rates.get(connKey(c))}
          <tr>
            <td class="mono">{hostPort(c.address, c.port)} <span class="muted">{c.proto}</span></td>
            <td class="mono">{c.remote}</td>
            <td class="prog">
              {#if c.owner}<span class="name">{ownerName(c.owner) || '?'}</span>{:else}<span class="muted">{c.user}</span>{/if}
            </td>
            <td class="num">{r ? `↑ ${rate(r.up)} ↓ ${rate(r.down)}` : '–'}</td>
            <td class="num">{c.proto === 'tcp' ? `↑ ${bytes(c.sent ?? 0)} ↓ ${bytes(c.received ?? 0)}` : '–'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if conns.length > limit}
    <button type="button" class="showmore" onclick={() => (limit += 300)}>{t('pkg.showMore', { n: conns.length - limit })}</button>
  {/if}
  {#if sockets?.truncated}<p class="foot">{t('net.truncated')}</p>{/if}
{/if}

<LineageDialog row={lineageOf} onclose={() => (lineageOf = null)} onchanged={() => setTimeout(() => void loadSockets(), 1000)} />

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
    gap: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    padding: var(--space-3);
    font-size: 13px;
    min-width: 0;
  }
  .card h2 {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-muted);
    margin: 0 0 var(--space-2);
  }
  .card p {
    margin: 2px 0 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .big {
    font-size: 15px;
    font-weight: 600;
  }
  .ok {
    color: var(--ok);
  }
  .warn {
    color: var(--warn);
  }
  .ifaces {
    margin-bottom: var(--space-4);
    font-size: 13px;
  }
  .ifaces summary {
    cursor: pointer;
    color: var(--text-muted);
    margin-bottom: var(--space-2);
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-4);
  }
  input[type='search'] {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    min-width: 220px;
  }
  .segmented {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow: hidden;
  }
  .segmented button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 0;
    border-inline-start: 1px solid var(--border);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .segmented button:first-child {
    border-inline-start: 0;
  }
  .segmented button.on {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .n {
    font-size: 11px;
    opacity: 0.75;
  }
  .note,
  .foot,
  .muted {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .foot {
    font-size: 12px;
    margin-top: var(--space-2);
  }
  .scroll {
    overflow-x: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  th,
  td {
    padding: 0 var(--space-3);
    height: var(--row-h);
    text-align: start;
    white-space: nowrap;
    border-bottom: 1px solid var(--border);
  }
  th {
    position: sticky;
    top: 0;
    background: var(--surface);
    font-weight: 600;
    color: var(--text-muted);
  }
  .num {
    text-align: end;
  }
  .wrap {
    white-space: normal;
  }
  tr.down {
    color: var(--text-muted);
  }
  .mono {
    font-family: var(--font-mono, monospace);
    font-size: 12px;
  }
  .port {
    width: 1%;
  }
  .prog {
    max-width: 320px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .name {
    font-weight: 500;
    margin-inline-end: var(--space-2);
  }
  .tag {
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
  }
  .tag.warn {
    color: var(--warn);
    border-color: var(--warn);
  }
  .act {
    width: 1%;
    text-align: end;
  }
  .what {
    font: inherit;
    font-size: 12px;
    color: var(--accent);
    background: none;
    border: 1px solid transparent;
    border-radius: var(--radius-s);
    padding: 2px 8px;
    cursor: pointer;
  }
  .what:hover,
  .what:focus-visible {
    border-color: var(--border);
  }
  .showmore {
    font: inherit;
    font-size: 13px;
    color: var(--accent);
    background: none;
    border: 0;
    padding: var(--space-2) 0;
    cursor: pointer;
  }
  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>

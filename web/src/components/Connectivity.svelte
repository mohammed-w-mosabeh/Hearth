<script lang="ts">
  import Chart from './Chart.svelte';
  import { api, ApiError } from '../lib/api';
  import { causeKey, parseTargets, targetsText, type ConnReport } from '../lib/connectivity';
  import { bytes } from '../lib/format';
  import type { Columns } from '../lib/history';
  import { locale, t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  // Connectivity (ROADMAP 6.5): off until the user turns it on, since it
  // sends traffic (SECURITY S21g). Data usage is counted either way.
  const REFRESH_MS = 60_000;
  const DAY_S = 86_400;

  let report = $state.raw<ConnReport | null>(null);
  let failed = $state('');
  let cols = $state.raw<Columns | null>(null);
  let now = $state(Date.now());
  let form = $state({ enabled: false, targets: '', dns: '', cap: '', iface: '' });
  let saving = $state(false);
  let loaded = false;

  async function load(): Promise<void> {
    try {
      const r = await api<ConnReport>('/connectivity');
      report = r;
      failed = '';
      if (!loaded) {
        loaded = true;
        form = { enabled: r.config.enabled, targets: targetsText(r.config.targets), dns: r.config.dns_name ?? '', cap: r.config.cap_gib ? String(r.config.cap_gib) : '', iface: r.config.cap_iface ?? '' };
      }
      if (r.config.enabled) {
        cols = await api<Columns>('/metrics/history?range=24h&m=conn.latency%3A');
        now = Date.now();
      }
    } catch (err) {
      failed = err instanceof ApiError ? err.message : t('conn.error');
    }
  }

  $effect(() => {
    void load();
    const timer = setInterval(() => void load(), REFRESH_MS);
    return () => clearInterval(timer);
  });

  async function save(): Promise<void> {
    saving = true;
    try {
      const cap = Number(form.cap);
      await api('/connectivity/config', {
        method: 'PUT',
        body: JSON.stringify({
          enabled: form.enabled,
          targets: parseTargets(form.targets),
          ...(form.dns.trim() ? { dns_name: form.dns.trim() } : {}),
          ...(form.cap.trim() && cap > 0 ? { cap_gib: cap, cap_iface: form.iface } : {}),
        }),
      });
      toast(t('conn.saved'));
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('conn.error'), { tone: 'error' });
    } finally {
      saving = false;
    }
  }

  const keys = $derived(Object.keys(cols?.series ?? {}).sort());
  const when = (ms: number) => new Date(ms).toLocaleString(locale('en-GB'));
</script>

<section class="conn" aria-labelledby="conn-title">
  <h2 id="conn-title">{t('conn.title')}</h2>
  {#if failed}<p class="error" role="status">{failed}</p>{/if}
  {#if report}
    {@const r = report}
    {#if r.config.enabled}
      <p class:bad={r.down} class="state" role="status">{r.down ? t('conn.down') : t('conn.up')}</p>
      <ul class="checks">
        {#each r.checks as c (c.kind + c.name)}
          <li>
            <span aria-hidden="true">{c.skipped ? '–' : c.ok ? '●' : '■'}</span>
            {c.kind === 'gateway' ? t('conn.gateway') : c.kind === 'dns' ? t('conn.dns') : c.name}:
            {c.skipped ? t('conn.skipped') : c.ok ? `${(c.ms ?? 0).toFixed(1)} ms` : t('conn.failed')}
          </li>
        {/each}
      </ul>
      {#if cols && keys.length > 0}
        <Chart title={t('conn.latency')} unit="ms" {keys} labels={keys.map((k) => k.replace('conn.latency:', ''))} {cols} xRange={[now / 1000 - DAY_S, now / 1000]} min={0} height={140} />
      {/if}
    {:else}
      <p class="note">{t('conn.off')}</p>
    {/if}

    <details open={!r.config.enabled}>
      <summary>{t('conn.setup')}</summary>
      <label class="check"><input type="checkbox" bind:checked={form.enabled} /> {t('conn.enable')}</label>
      <label>{t('conn.targets')}<textarea rows="3" spellcheck="false" bind:value={form.targets} placeholder="example.com:443"></textarea></label>
      <p class="note">{t('conn.targetsHelp')}</p>
      <label>{t('conn.dnsName')}<input type="text" spellcheck="false" bind:value={form.dns} /></label>
      <label>{t('conn.cap')}<input type="number" min="0" step="any" bind:value={form.cap} /></label>
      <label>{t('conn.capIface')}
        <select bind:value={form.iface}>
          <option value=""></option>
          {#each r.usage as u (u.iface)}<option value={u.iface}>{u.iface}</option>{/each}
        </select>
      </label>
      <button type="button" disabled={saving} onclick={save}>{t('conn.save')}</button>
    </details>

    {#if r.outages.length > 0}
      <h3>{t('conn.outages')}</h3>
      <ul class="plain">
        {#each r.outages as o (o.id)}
          <li>{when(o.started_at)} – {o.ended_at ? when(o.ended_at) : t('conn.ongoing')} <span class="note">{t(causeKey(o.cause))}</span></li>
        {/each}
      </ul>
    {/if}

    <h3>{t('conn.usage')}</h3>
    {#if r.usage.length === 0}
      <p class="note">{t('conn.noUsage')}</p>
    {:else}
      <div class="table">
        <table>
          <thead><tr><th>{t('conn.col.iface')}</th><th class="num">{t('conn.col.today')}</th><th class="num">{t('conn.col.month')}</th></tr></thead>
          <tbody>
            {#each r.usage as u (u.iface)}
              <tr><td>{u.iface}</td><td class="num">↓ {bytes(u.today_rx)} ↑ {bytes(u.today_tx)}</td><td class="num">↓ {bytes(u.month_rx)} ↑ {bytes(u.month_tx)}</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</section>

<style>
  .conn {
    margin-bottom: var(--space-5);
  }
  h2 {
    font-size: 15px;
    margin-bottom: var(--space-2);
  }
  h3 {
    font-size: 13px;
    margin: var(--space-3) 0 var(--space-1);
  }
  .state {
    margin: 0 0 var(--space-1);
    font-weight: 600;
    color: var(--ok);
  }
  .state.bad {
    color: var(--crit);
  }
  .note {
    margin: 0;
    font-size: 12px;
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
    font-size: 13px;
  }
  ul {
    list-style: none;
    margin: 0 0 var(--space-2);
    padding: 0;
    font-size: 13px;
  }
  .checks {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1) var(--space-4);
  }
  li {
    overflow-wrap: anywhere;
  }
  details {
    margin: var(--space-2) 0;
    font-size: 13px;
  }
  summary {
    cursor: pointer;
    color: var(--text-muted);
  }
  label {
    display: grid;
    gap: 2px;
    margin-top: var(--space-2);
    max-inline-size: 420px;
  }
  label.check {
    display: flex;
    gap: var(--space-2);
    align-items: center;
  }
  input:not([type='checkbox']),
  textarea,
  select,
  button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  button {
    margin-top: var(--space-2);
    cursor: pointer;
  }
  .table {
    overflow-x: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  th,
  td {
    padding: var(--space-1) var(--space-3);
    text-align: start;
    white-space: nowrap;
  }
  th {
    color: var(--text-muted);
  }
  .num {
    text-align: end;
  }
</style>

<script lang="ts">
  import { failure } from '../lib/errors';
  import AccessCard from '../components/AccessCard.svelte';
  import IntegrityCard from '../components/IntegrityCard.svelte';
  import LynisCard from '../components/LynisCard.svelte';
  import { api } from '../lib/api';
  import { locale, t } from '../lib/i18n';
  import {
    checkHint,
    checkTitle,
    cveRows,
    findingKey,
    noneKey,
    overall,
    riskKey,
    statusLabel,
    trackerURL,
    verdictKey,
    type CVEs,
    type Check,
    type Finding,
    type Report,
  } from '../lib/security';
  import { toast } from '../lib/toast.svelte';

  let report = $state.raw<Report | null>(null);
  let failed = $state('');
  let loading = $state(false);
  let cvesRunning = $state(false);
  let onlyFixed = $state(true);

  async function load(): Promise<void> {
    loading = true;
    try {
      report = await api<Report>('/security');
      failed = '';
    } catch (err) {
      failed = failure(err, 'security.error');
    } finally {
      loading = false;
    }
  }

  async function runCVEs(): Promise<void> {
    cvesRunning = true;
    try {
      const v = await api<CVEs>('/security/cves', { method: 'POST' });
      if (v.error) toast(t('security.cves.failed', { s: v.error }), { tone: 'error' });
      await load();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      cvesRunning = false;
    }
  }

  $effect(() => {
    void load();
  });

  function text(f: Finding): string {
    const key = findingKey(f.code);
    return key ? t(key, { s: f.subject ?? '' }) : `${f.code} ${f.subject ?? ''}`.trim();
  }

  const when = (ms: number) => new Date(ms).toLocaleString(locale());
  const verdict = $derived(report ? overall(report) : null);
  const shownCVEs = $derived(report ? cveRows(report.cves, onlyFixed) : []);
</script>

{#snippet findingRow(c: Check, f: Finding)}
  <li class="finding {f.severity}">
    <span class="dot" aria-hidden="true"></span>
    <span class="body">
      <span>{text(f)}</span>
      {#if f.details?.length}
        <span class="details">
          {#if c.id === 'containers'}
            {#each f.details as r (r)}
              {@const rk = riskKey(r)}
              <span class="chip">{t(rk.key)}{#if rk.path}&nbsp;<code>{rk.path}</code>{/if}</span>
            {/each}
          {:else}
            {#each f.details as d (d)}<code>{d}</code>{/each}
          {/if}
        </span>
      {/if}
    </span>
  </li>
{/snippet}

<header>
  <div>
    <h1>{t('security.title')}</h1>
    <p>{t('security.intro')}</p>
  </div>
  <button type="button" class="btn" onclick={() => load()} disabled={loading}>{t('security.refresh')}</button>
</header>

{#if failed}
  <p class="note error" role="status">{failed}</p>
{:else if report === null}
  <p class="note">{t('security.loading')}</p>
{:else}
  <section class="summary {verdict}" aria-busy={loading}>
    <strong>{t(verdictKey(verdict ?? 'ok'))}</strong>
    <span>
      {t('security.counts', { crit: report.counts.critical ?? 0, warn: report.counts.warning ?? 0, ok: report.counts.ok ?? 0 })}
      · {t('security.checked', { time: when(report.time) })}
    </span>
  </section>

  <div class="checks">
    {#each report.checks as c (c.id)}
      {@const hint = checkHint(c.id)}
      <article class="check" aria-labelledby={`check-${c.id}`}>
        <div class="head">
          <h2 id={`check-${c.id}`}>{t(checkTitle(c.id))}</h2>
          <span class="badge {c.status}">{t(statusLabel(c.status))}</span>
        </div>
        {#if c.error}
          <p class="note">{t('security.unavailable', { s: c.error })}</p>
        {/if}
        {#if c.findings.length === 0 && c.status === 'ok'}
          <p class="note">{t(noneKey(c.id))}</p>
        {:else if c.findings.length > 0}
          <ul class="findings">
            {#each c.findings as f, i (i)}
              {@render findingRow(c, f)}
            {/each}
          </ul>
        {/if}

        {#if c.id === 'ssh' && report.ssh}
          {@const s = report.ssh}
          <dl class="kv">
            <div><dt>PermitRootLogin</dt><dd>{s.permit_root_login}</dd></div>
            <div><dt>PasswordAuthentication</dt><dd>{s.password_authentication}</dd></div>
            <div><dt>KbdInteractiveAuthentication</dt><dd>{s.kbd_interactive_authentication}</dd></div>
            <div><dt>PermitEmptyPasswords</dt><dd>{s.permit_empty_passwords}</dd></div>
            <div><dt>Port</dt><dd>{s.ports.join(', ')}</dd></div>
            <div><dt>ListenAddress</dt><dd>{s.listen_addresses.length ? s.listen_addresses.join(', ') : t('security.ssh.all')}</dd></div>
          </dl>
        {/if}

        {#if c.id === 'cves'}
          {@const v = report.cves}
          <div class="cves">
            {#if v.installed}
              <div class="row">
                <button type="button" class="btn" onclick={runCVEs} disabled={cvesRunning} aria-busy={cvesRunning}>
                  {cvesRunning ? t('security.cves.running') : v.time ? t('security.cves.again') : t('security.cves.run')}
                </button>
                <span class="note">{v.time ? t('security.cves.last', { time: when(v.time), suite: v.suite ?? '' }) : t('security.cves.privacy')}</span>
              </div>
              {#if v.list.length > 0}
                <label class="toggle"><input type="checkbox" bind:checked={onlyFixed} /> {t('security.cves.onlyFixed')}</label>
                {#if shownCVEs.length > 0}
                  <div class="table">
                    <table>
                      <thead>
                        <tr><th>{t('security.cves.id')}</th><th>{t('security.cves.package')}</th><th>{t('security.cves.urgency')}</th><th>{t('security.cves.notes')}</th></tr>
                      </thead>
                      <tbody>
                        {#each shownCVEs as x (x.id + x.package)}
                          {@const url = trackerURL(x.id)}
                          <tr>
                            <td>{#if url}<a href={url} target="_blank" rel="noopener noreferrer">{x.id}</a>{:else}{x.id}{/if}</td>
                            <td>{x.package}</td>
                            <td>{x.urgency ?? ''}</td>
                            <td>
                              {#if x.fixed}<span class="chip ok">{t('security.cves.fixAvailable')}</span>{/if}
                              {#if x.remote}<span class="chip crit">{t('security.cves.remote')}</span>{/if}
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                  {#if (onlyFixed ? v.fixed : v.list.length) > shownCVEs.length}
                    <p class="note">{t('security.cves.more', { n: shownCVEs.length })}</p>
                  {/if}
                {/if}
              {/if}
            {:else}
              <p class="note">{t('security.cves.install')} <code>sudo apt install debsecan</code></p>
            {/if}
          </div>
        {/if}

        {#if hint && (c.status === 'warning' || c.status === 'critical')}
          <p class="hint">{t(hint)}</p>
        {/if}
      </article>
    {/each}
  </div>
{/if}

<LynisCard />
<IntegrityCard />
<AccessCard />

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
  header p,
  .note {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
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
  .summary {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: baseline;
    padding: var(--space-3) var(--space-4);
    border-radius: var(--radius-m);
    border: 1px solid var(--border);
    border-inline-start: 4px solid var(--ok);
    background: var(--surface);
    margin-bottom: var(--space-4);
  }
  .summary.warning {
    border-inline-start-color: var(--warn);
  }
  .summary.critical {
    border-inline-start-color: var(--crit);
  }
  .summary.unknown {
    border-inline-start-color: var(--text-muted);
  }
  .summary span {
    color: var(--text-muted);
  }
  .checks {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
    gap: var(--space-4);
    align-items: start;
  }
  .check {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    min-width: 0;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-2);
  }
  h2 {
    font-size: 15px;
    margin: 0;
  }
  .badge {
    font-size: 12px;
    padding: 1px var(--space-2);
    border-radius: 999px;
    border: 1px solid currentColor;
    color: var(--ok);
    white-space: nowrap;
  }
  .badge.warning {
    color: var(--warn);
  }
  .badge.critical {
    color: var(--crit);
  }
  .badge.unknown,
  .badge.off {
    color: var(--text-muted);
  }
  .findings {
    list-style: none;
    padding: 0;
    margin: var(--space-2) 0 0;
  }
  .finding {
    display: flex;
    gap: var(--space-2);
    padding: var(--space-1) 0;
    overflow-wrap: anywhere;
  }
  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    margin-top: 7px;
    background: var(--text-muted);
  }
  .finding.warning .dot {
    background: var(--warn);
  }
  .finding.critical .dot {
    background: var(--crit);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .details {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
  }
  code {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--text-muted);
  }
  .chip {
    font-size: 12px;
    padding: 0 var(--space-2);
    border-radius: var(--radius-s);
    background: var(--surface-2);
  }
  .chip.ok {
    color: var(--ok);
  }
  .chip.crit {
    color: var(--crit);
  }
  .hint {
    font-size: 13px;
    margin: var(--space-2) 0 0;
    padding-top: var(--space-2);
    border-top: 1px solid var(--border);
    color: var(--text-muted);
  }
  .kv {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: var(--space-1) var(--space-3);
    margin: var(--space-2) 0 0;
  }
  dt {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    font-size: 13px;
  }
  .cves .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: center;
    margin-top: var(--space-2);
  }
  .cves .row .note {
    margin: 0;
  }
  .toggle {
    display: inline-flex;
    gap: var(--space-2);
    align-items: center;
    margin: var(--space-2) 0;
    font-size: 13px;
  }
  .table {
    max-height: 320px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th,
  td {
    text-align: start;
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border);
  }
  th {
    color: var(--text-muted);
    font-weight: 600;
    position: sticky;
    top: 0;
    background: var(--surface);
  }
  a {
    color: var(--accent);
  }
</style>

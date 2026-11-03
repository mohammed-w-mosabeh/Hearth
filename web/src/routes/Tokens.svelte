<script lang="ts">
  import Skeleton from '../components/Skeleton.svelte';
  import { failure } from '../lib/errors';
  import type { Plan } from '../lib/actions';
  import { api } from '../lib/api';
  import { locale, t } from '../lib/i18n';
  import { scrapeConfig, validName, type TokenInfo, type TokensView } from '../lib/tokens';
  import { toast } from '../lib/toast.svelte';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';

  // Read-only tokens for the hearth command and /metrics (ROADMAP 7.5). The
  // page never sees a token: it lives in the file named here.
  let view = $state.raw<TokensView | null>(null);
  let failed = $state('');
  let name = $state('cli');
  let busy = $state(false);
  let revoking = $state.raw<TokenInfo | null>(null);

  async function load(): Promise<void> {
    try {
      view = await api<TokensView>('/tokens');
      failed = '';
    } catch (err) {
      failed = failure(err, 'tokens.error');
    }
  }

  async function change(run: () => Promise<unknown>): Promise<void> {
    busy = true;
    try {
      await run();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
      await load();
    }
  }

  async function create(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    await change(async () => {
      await api('/tokens', { method: 'POST', body: JSON.stringify({ name }) });
      name = '';
    });
  }

  async function revoke(): Promise<void> {
    const token = revoking;
    revoking = null;
    if (token) await change(() => api(`/tokens/${encodeURIComponent(token.name)}`, { method: 'DELETE' }));
  }

  async function setMetrics(enabled: boolean): Promise<void> {
    await change(() => api('/tokens/metrics', { method: 'PUT', body: JSON.stringify({ enabled }) }));
  }

  $effect(() => {
    void load();
  });

  const when = (ms: number) => new Date(ms).toLocaleString(locale());
  const nameOk = $derived(validName(name));
  const plan = $derived<Plan | null>(
    revoking ? { summary: '', items: [{ id: revoking.name, name: revoking.name }], blocked: [], risk: 'safe' } : null,
  );
  const hasCli = $derived(view?.tokens.some((k) => k.name === 'cli') ?? false);
</script>

<header>
  <h1>{t('tokens.title')}</h1>
  <p class="muted">{t('tokens.intro')}</p>
</header>

{#if failed}
  <p class="muted" role="status">{failed}</p>
{:else if view === null}
  <Skeleton label={t('security.loading')} />
{:else}
  <section class="card" aria-labelledby="tokens-list">
    <h2 id="tokens-list">{t('tokens.list')}</h2>
    {#if view.tokens.length === 0}
      <p class="muted">{t('tokens.none')}</p>
    {:else}
      <table>
        <thead>
          <tr><th>{t('tokens.name')}</th><th>{t('tokens.file')}</th><th>{t('tokens.created')}</th><th></th></tr>
        </thead>
        <tbody>
          {#each view.tokens as k (k.name)}
            <tr>
              <td>{k.name}</td>
              <td><code dir="ltr">{k.file}</code></td>
              <td>{k.created ? when(k.created) : ''}</td>
              <td><button type="button" class="link" disabled={busy} onclick={() => (revoking = k)}>{t('tokens.revoke')}</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <form class="row" onsubmit={create}>
      <label for="token-name">{t('tokens.name')}</label>
      <input id="token-name" bind:value={name} maxlength="32" autocomplete="off" spellcheck="false" />
      <button type="submit" class="btn" disabled={busy || !nameOk}>{t('tokens.create')}</button>
    </form>
    <p class="muted">{t('tokens.nameHint')}</p>
  </section>

  <section class="card" aria-labelledby="tokens-cli">
    <h2 id="tokens-cli">{t('tokens.cli')}</h2>
    <p class="muted">{t(hasCli ? 'tokens.cliReady' : 'tokens.cliNeeds')}</p>
    <pre dir="ltr">hearth status
hearth ports
hearth alerts
hearth units
hearth containers --json</pre>
  </section>

  <section class="card" aria-labelledby="tokens-metrics">
    <h2 id="tokens-metrics">{t('tokens.metrics')}</h2>
    <p class="muted">{t('tokens.metricsIntro')}</p>
    <label class="row">
      <input type="checkbox" checked={view.metrics} disabled={busy} onchange={(e) => setMetrics(e.currentTarget.checked)} />
      {t('tokens.metricsOn')}
    </label>
    {#if view.metrics}
      <p class="muted">{t('tokens.metricsHow')}</p>
      <pre dir="ltr">{scrapeConfig(view.port, view.tokens[0]?.file ?? '/path/to/name.token')}</pre>
    {/if}
  </section>
{/if}

<ConfirmDialog
  {plan}
  title={t('tokens.revokeTitle')}
  verb={t('tokens.revoke')}
  body={t('tokens.revokeBody')}
  onconfirm={revoke}
  oncancel={() => (revoking = null)}
/>

<style>
  header {
    margin-bottom: var(--space-3);
  }
  h1 {
    margin: 0;
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-top: var(--space-4);
  }
  h2 {
    margin: 0 0 var(--space-2);
    font-size: 1rem;
  }
  .muted {
    color: var(--text-muted);
  }
  .row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-3);
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: start;
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border);
    overflow-wrap: anywhere;
  }
  input:not([type='checkbox']) {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  pre {
    margin: var(--space-2) 0 0;
    padding: var(--space-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow-x: auto;
    font-size: 13px;
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
  }
  .link {
    font: inherit;
    cursor: pointer;
    background: none;
    border: none;
    padding: 0;
    color: var(--accent);
  }
</style>

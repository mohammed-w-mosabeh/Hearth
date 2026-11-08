<script lang="ts">
  import { api } from '../lib/api';
  import { reasonKey, run, type Result } from '../lib/actions';
  import { failure } from '../lib/errors';
  import { t } from '../lib/i18n';
  import { proxyURL, type ProxyStatus } from '../lib/stacks';
  import { toast } from '../lib/toast.svelte';

  // An app's local name (ROADMAP 9.13): http://<app>.localhost:<port>/
  // opens a port the app publishes. Hearth serves names on 127.0.0.1 only,
  // never for itself, and only once the owner turns names on.
  let { name }: { name: string } = $props();

  let st = $state.raw<ProxyStatus | null>(null);
  let error = $state('');
  let busy = $state(false);
  let choice = $state('0');
  let port = $state(8470);

  async function load(): Promise<void> {
    try {
      st = await api<ProxyStatus>('/proxy');
      choice = String(st.routes[name] ?? 0);
      port = st.port;
      error = '';
    } catch (err) {
      error = failure(err, 'stacks.proxyUnread');
    }
  }

  $effect(() => {
    void name;
    void load();
  });

  const ports = $derived(st?.ports[name] ?? []);
  const route = $derived(st?.routes[name] ?? 0);

  function report(res: Result, done: string): void {
    const first = [...res.blocked, ...res.failed][0];
    if (!first) {
      toast(done);
      return;
    }
    toast(t('stacks.failed', { name, reason: first.detail || t(reasonKey(first.reason), { detail: '', user: '' }) }), { tone: 'error' });
  }

  async function act(action: 'route' | 'listen', target: string, params: Record<string, string>, done: string): Promise<void> {
    busy = true;
    try {
      report(await run('proxy', action, target, params), done);
      await load();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

{#if error}
  <p class="muted">{error}</p>
{:else if st}
  {#if ports.length === 0}
    <p class="muted">{t('stacks.proxyNoPorts')}</p>
  {:else}
    <div class="row">
      <label>
        {t('stacks.proxyPort')}
        <select bind:value={choice} disabled={busy}>
          <option value="0">{t('stacks.proxyNone')}</option>
          {#each ports as p (p)}
            <option value={String(p)}>127.0.0.1:{p}</option>
          {/each}
        </select>
      </label>
      <button
        type="button"
        class="plain"
        disabled={busy || choice === String(route)}
        onclick={() => act('route', name, { port: choice }, t(choice === '0' ? 'stacks.proxyRemoved' : 'stacks.proxySaved', { name }))}
      >
        {t('stacks.proxySave')}
      </button>
    </div>
    {#if route && st.running}
      <p><a href={proxyURL(name, st.port)} target="_blank" rel="noopener noreferrer">{proxyURL(name, st.port)}</a></p>
    {:else if route}
      <p class="muted">{t('stacks.proxyWaiting', { url: proxyURL(name, st.port) })}</p>
    {/if}
  {/if}
  <div class="row">
    {#if st.enabled}
      <p class="muted">{st.running ? t('stacks.proxyOn', { port: st.port }) : t('stacks.proxyDown', { why: st.error ?? '' })}</p>
      <button type="button" class="plain" disabled={busy} onclick={() => act('listen', 'proxy', { enabled: 'false' }, t('stacks.proxyStopped'))}>
        {t('stacks.proxyTurnOff')}
      </button>
    {:else}
      <p class="muted">{t('stacks.proxyOff')}</p>
      <label>
        {t('stacks.proxyListen')}
        <input type="number" min="1024" max="65535" bind:value={port} disabled={busy} />
      </label>
      <button
        type="button"
        class="plain"
        disabled={busy || !Number.isInteger(port) || port < 1024 || port > 65535}
        onclick={() => act('listen', 'proxy', { enabled: 'true', port: String(port) }, t('stacks.proxyStarted', { port }))}
      >
        {t('stacks.proxyTurnOn')}
      </button>
    {/if}
  </div>
{/if}

<style>
  .row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-2);
    font-size: 13px;
  }
  p {
    margin: 0 0 var(--space-2);
    font-size: 13px;
  }
  input {
    width: 7em;
  }
</style>

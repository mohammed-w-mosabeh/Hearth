<script lang="ts">
  import Sidebar from './components/Sidebar.svelte';
  import EmptyState from './components/EmptyState.svelte';
  import Home from './routes/Home.svelte';
  import Placeholder from './routes/Placeholder.svelte';
  import { api, setSession } from './lib/api';
  import { t } from './lib/i18n';
  import { href, router, routes } from './lib/router.svelte';
  import { loadKey } from './lib/session';
  import { Live, type WsState } from './lib/ws';

  let key = $state(loadKey({ location, history, storage: localStorage }));
  let status = $state<WsState>('connecting');

  setSession(
    () => key,
    () => (key = null),
  );

  $effect(() => {
    if (key === null) return;
    // A stored key dies when hearthd restarts; a 401 here clears it and
    // shows the "open from the launcher" screen.
    api('/session').catch(() => {});
    const live = new Live(key, (s) => (status = s));
    return () => live.close();
  });

  const route = $derived(routes.find((r) => r.id === router.id));
</script>

{#if key === null}
  <main class="centered">
    <EmptyState title={t('auth.title')} body={t('auth.body')} />
  </main>
{:else}
  <div class="shell">
    <Sidebar {status} />
    <main>
      {#if route?.id === 'home'}
        <Home />
      {:else if route}
        <Placeholder {route} />
      {:else}
        <EmptyState title={t('notfound.title')} body="">
          <a href={href('home')}>{t('notfound.back')}</a>
        </EmptyState>
      {/if}
    </main>
  </div>
{/if}

<style>
  .shell {
    display: flex;
    height: 100%;
  }
  main {
    flex: 1;
    overflow: auto;
    padding: var(--space-5) var(--space-6);
  }
  .centered {
    height: 100%;
  }
</style>

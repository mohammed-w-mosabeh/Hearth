<script lang="ts">
  import CommandPalette from './components/CommandPalette.svelte';
  import Sidebar from './components/Sidebar.svelte';
  import EmptyState from './components/EmptyState.svelte';
  import Toaster from './components/Toaster.svelte';
  import Home from './routes/Home.svelte';
  import Alerts from './routes/Alerts.svelte';
  import Applications from './routes/Applications.svelte';
  import Resources from './routes/Resources.svelte';
  import Services from './routes/Services.svelte';
  import Placeholder from './routes/Placeholder.svelte';
  import { api, setSession } from './lib/api';
  import { t } from './lib/i18n';
  import { href, router, routes } from './lib/router.svelte';
  import { loadKey } from './lib/session';
  import { Live, type WsState } from './lib/ws';

  let key = $state(loadKey({ location, history, storage: localStorage }));
  let status = $state<WsState>('connecting');
  let live = $state.raw<Live | null>(null);

  setSession(
    () => key,
    () => (key = null),
  );

  $effect(() => {
    if (key === null) return;
    // A stored key dies when hearthd restarts; a 401 here clears it and
    // shows the "open from the launcher" screen.
    api('/session').catch(() => {});
    const l = new Live(key, (s) => (status = s));
    live = l;
    return () => {
      l.close();
      live = null;
    };
  });

  const route = $derived(routes.find((r) => r.id === router.id));

  // Ctrl+K (Cmd+K on a Mac keyboard) opens the command palette anywhere.
  let palette = $state(false);
  function onkeydown(e: KeyboardEvent): void {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k' && key !== null) {
      e.preventDefault();
      palette = !palette;
    }
  }
</script>

<svelte:window {onkeydown} />

{#if key === null}
  <main class="centered">
    <EmptyState title={t('auth.title')} body={t('auth.body')} />
  </main>
{:else}
  <div class="shell">
    <Sidebar {status} onsearch={() => (palette = true)} />
    <main>
      {#if route?.id === 'home'}
        {#if live}<Home {live} />{/if}
      {:else if route?.id === 'apps'}
        {#if live}<Applications {live} />{/if}
      {:else if route?.id === 'resources'}
        {#if live}<Resources {live} />{/if}
      {:else if route?.id === 'services'}
        {#if live}<Services {live} />{/if}
      {:else if route?.id === 'alerts'}
        {#if live}<Alerts {live} />{/if}
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

<Toaster />
<CommandPalette open={palette && key !== null} {live} onclose={() => (palette = false)} />

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

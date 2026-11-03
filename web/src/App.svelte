<script lang="ts">
  import Hardware from './routes/Hardware.svelte';
  import CommandPalette from './components/CommandPalette.svelte';
  import EmberRail from './components/EmberRail.svelte';
  import Inspector from './components/Inspector.svelte';
  import PulseStrip from './components/PulseStrip.svelte';
  import Sidebar from './components/Sidebar.svelte';
  import EmptyState from './components/EmptyState.svelte';
  import Toaster from './components/Toaster.svelte';
  import Home from './routes/Home.svelte';
  import Alerts from './routes/Alerts.svelte';
  import Applications from './routes/Applications.svelte';
  import Games from './routes/Games.svelte';
  import Changes from './routes/Changes.svelte';
  import Profiles from './routes/Profiles.svelte';
  import Insight from './routes/Insight.svelte';
  import Journal from './routes/Journal.svelte';
  import Reports from './routes/Reports.svelte';
  import Space from './routes/Space.svelte';
  import Security from './routes/Security.svelte';
  import Tokens from './routes/Tokens.svelte';
  import Settings from './routes/Settings.svelte';
  import Disks from './routes/Disks.svelte';
  import Docker from './routes/Docker.svelte';
  import Packages from './routes/Packages.svelte';
  import Tools from './routes/Tools.svelte';
  import Network from './routes/Network.svelte';
  import Terminal from './routes/Terminal.svelte';
  import Scheduler from './routes/Scheduler.svelte';
  import Resources from './routes/Resources.svelte';
  import Services from './routes/Services.svelte';
  import ShortcutsDialog from './components/ShortcutsDialog.svelte';
  import { api, setSession } from './lib/api';
  import { emberRequested } from './lib/ember';
  import { t } from './lib/i18n';
  import { href, pageOf, router, routes } from './lib/router.svelte';
  import { Sequence } from './lib/shortcuts';
  import { loadKey } from './lib/session';
  import { Live, type WsState } from './lib/ws';

  const ember = emberRequested(location.search);
  let key = $state(loadKey({ location, history, storage: localStorage }));
  let status = $state<WsState>('connecting');
  let beat = $state(0);
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
    const l = new Live(
      key,
      (s) => (status = s),
      () => beat++,
    );
    live = l;
    return () => {
      l.close();
      live = null;
    };
  });

  const route = $derived(routes.find((r) => r.id === pageOf(router.id)));

  // The Terminal page stays mounted once visited, so its shells survive
  // a visit to another page.
  let terminalOpened = $state(false);
  $effect(() => {
    if (route?.id === 'terminal') terminalOpened = true;
  });

  // Ctrl+K (Cmd+K on a Mac keyboard) opens the command palette anywhere.
  let palette = $state(false);
  let keysHelp = $state(false);
  const sequence = new Sequence();
  function onkeydown(e: KeyboardEvent): void {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k' && key !== null) {
      e.preventDefault();
      palette = !palette;
      return;
    }
    if (key === null || palette || keysHelp) return;
    const out = sequence.press(e, Date.now());
    if (out === null) return;
    e.preventDefault();
    if ('help' in out) keysHelp = true;
    else location.hash = href(out.go);
  }
</script>

<svelte:window {onkeydown} />

{#if key === null}
  <main class="centered">
    <EmptyState title={t('auth.title')} body={t('auth.body')} />
  </main>
{:else}
  <div class="shell">
    {#if ember}
      <EmberRail {status} {beat} onsearch={() => (palette = true)} />
    {:else}
      <Sidebar {status} onsearch={() => (palette = true)} />
    {/if}
    <main>
      {#if ember && live}<PulseStrip {live} />{/if}
      {#if ember && live}<Inspector {live} />{/if}
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
      {:else if route?.id === 'disks'}
        {#if live}<Disks {live} />{/if}
      {:else if route?.id === 'docker'}
        {#if live}<Docker {live} />{/if}
      {:else if route?.id === 'packages'}
        {#if live}<Packages {live} />{/if}
      {:else if route?.id === 'settings'}
        <Settings />
      {:else if route?.id === 'games'}
        <Games />
      {:else if route?.id === 'reports'}
        <Reports />
      {:else if route?.id === 'profiles'}
        {#if live}<Profiles {live} />{/if}
      {:else if route?.id === 'hardware'}
        <Hardware />
      {:else if route?.id === 'space'}
        <Space />
      {:else if route?.id === 'insight'}
        <Insight />
      {:else if route?.id === 'journal'}
        <Journal />
      {:else if route?.id === 'changes'}
        <Changes />
      {:else if route?.id === 'tokens'}
        <Tokens />
      {:else if route?.id === 'security'}
        <Security />
      {:else if route?.id === 'tools'}
        <Tools />
      {:else if route?.id === 'network'}
        <Network />
      {:else if route?.id === 'scheduler'}
        <Scheduler />
      {:else if route?.id === 'terminal'}
        <!-- below, kept mounted -->
      {:else if !route}
        <EmptyState title={t('notfound.title')} body="">
          <a href={href('home')}>{t('notfound.back')}</a>
        </EmptyState>
      {/if}
      {#if terminalOpened}
        <div class="terminal" hidden={route?.id !== 'terminal'}>
          <Terminal active={route?.id === 'terminal'} />
        </div>
      {/if}
    </main>
  </div>
{/if}

<Toaster />
<CommandPalette open={palette && key !== null} {live} onclose={() => (palette = false)} />
<ShortcutsDialog open={keysHelp} onclose={() => (keysHelp = false)} />

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
  .terminal {
    height: 100%;
  }
  .terminal[hidden] {
    display: none;
  }
</style>

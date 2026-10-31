<script lang="ts">
  import { api } from '../lib/api';
  import { t } from '../lib/i18n';
  import { href } from '../lib/router.svelte';
  import { restartKey, type Restart } from '../lib/security';

  // Home's reminder (ROADMAP 5.6). Silent when nothing waits, or when this
  // could not be read: the Security page says why.
  let info = $state.raw<Restart | null>(null);

  $effect(() => {
    api<Restart>('/restart')
      .then((r) => (info = r))
      .catch(() => {});
  });

  const key = $derived(info ? restartKey(info) : null);
</script>

{#if key && info}
  <p class="banner" role="status">
    <span>{t(key, { n: info.services.length })}</span>
    <a href={href('security')}>{t('home.restart.details')}</a>
  </p>
{/if}

<style>
  .banner {
    display: flex;
    justify-content: space-between;
    gap: var(--space-3);
    margin: 0 0 var(--space-3);
    padding: var(--space-2) var(--space-3);
    font-size: 13px;
    color: var(--warn);
    border: 1px solid var(--warn);
    border-radius: var(--radius-s);
  }
  a {
    color: inherit;
    white-space: nowrap;
  }
</style>

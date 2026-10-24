<script lang="ts">
  import EmptyState from '../components/EmptyState.svelte';
  import { t } from '../lib/i18n';
  import type { Route } from '../lib/router.svelte';

  let { route }: { route: Route } = $props();
</script>

<EmptyState title={t(route.label)} body={t('page.soon', { page: t(route.label), phase: route.phase })}>
  <p>{t('page.soon.hint')}</p>
</EmptyState>

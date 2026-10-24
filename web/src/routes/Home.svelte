<script lang="ts">
  import { t } from '../lib/i18n';
</script>

<header>
  <h1>{t('home.title')}</h1>
  <p>{t('home.intro')}</p>
</header>

<style>
  header {
    margin-bottom: var(--space-5);
  }
  h1 {
    font-size: 22px;
  }
  p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
</style>

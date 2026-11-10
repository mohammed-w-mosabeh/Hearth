<script lang="ts">
  import { api } from '../lib/api';
  import { failure } from '../lib/errors';
  import { t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  // "Pause all automations" (SECURITY S15d), on the Automations page and on
  // Home: while paused nothing evaluates or runs, alert notifications
  // included. paused is bound so the page can follow it.
  let { paused = $bindable(false) }: { paused?: boolean } = $props();

  let known = $state(false);
  let busy = $state(false);

  $effect(() => {
    api<{ paused: boolean }>('/automations/pause').then(
      (r) => {
        paused = r.paused;
        known = true;
      },
      () => {},
    );
  });

  async function set(on: boolean): Promise<void> {
    busy = true;
    try {
      const r = await api<{ paused: boolean }>('/automations/pause', { method: 'PUT', body: JSON.stringify({ paused: on }) });
      paused = r.paused;
      toast(t(r.paused ? 'auto.paused.on' : 'auto.paused.off'));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

{#if known}
  <section class="card pause" class:on={paused} aria-label={t('auto.pause')}>
    <label class="switch">
      <input type="checkbox" checked={paused} disabled={busy} onchange={(e) => set(e.currentTarget.checked)} />
      {t('auto.pause')}
    </label>
    <p class="note">{paused ? t('auto.pause.onNote') : t('auto.pause.note')}</p>
  </section>
{/if}

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-bottom: var(--space-3);
  }
  .on {
    border-inline-start: 4px solid var(--warn);
  }
  .switch {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    font-size: 13px;
    font-weight: 600;
  }
  .note {
    color: var(--text-muted);
    font-size: 13px;
    margin: var(--space-1) 0 0;
  }
</style>

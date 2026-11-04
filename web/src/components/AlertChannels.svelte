<script lang="ts">
  import { failure } from '../lib/errors';
  import { channelBody, channelForm, type ChannelForm, type ChannelView } from '../lib/alerts';
  import { api } from '../lib/api';
  import { t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  // ntfy and mail (ROADMAP 5.4). The server never sends a secret back: the
  // fields start empty, and empty keeps what is saved.
  let view = $state.raw<ChannelView | null>(null);
  let form = $state<ChannelForm | null>(null);
  let busy = $state(false);

  const problem = (err: unknown) => (failure(err, 'action.failed'));

  function show(v: ChannelView): void {
    view = v;
    form = channelForm(v);
  }

  $effect(() => {
    api<ChannelView>('/alerts/channels')
      .then(show)
      .catch((err: unknown) => toast(problem(err), { tone: 'error' }));
  });

  async function save(): Promise<boolean> {
    if (!form || !view) return false;
    busy = true;
    try {
      show(await api<ChannelView>('/alerts/channels', { method: 'PUT', body: JSON.stringify(channelBody(form, view)) }));
      return true;
    } catch (err) {
      toast(problem(err), { tone: 'error' });
      return false;
    } finally {
      busy = false;
    }
  }

  async function test(channel: 'ntfy' | 'smtp'): Promise<void> {
    if (!(await save())) return;
    busy = true;
    try {
      await api(`/alerts/test?channel=${channel}`, { method: 'POST' });
      toast(t('alerts.channels.testSent'));
    } catch (err) {
      toast(problem(err), { tone: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

{#if form && view}
  <section>
    <h2>{t('alerts.channels')}</h2>
    <p class="hint">{t('alerts.channels.intro')}</p>
    <form
      onsubmit={async (e) => {
        e.preventDefault();
        if (await save()) toast(t('alerts.channels.savedToast'));
      }}
    >
      <div class="grid">
        <fieldset>
          <legend>{t('alerts.channels.ntfy')}</legend>
          <label class="check"><input type="checkbox" bind:checked={form.ntfy.enabled} /> {t('alerts.channels.enabled')}</label>
          <label>{t('alerts.channels.server')} <input type="url" bind:value={form.ntfy.server} placeholder="https://ntfy.sh" maxlength="255" /></label>
          <label>
            {t('alerts.channels.topic')}
            <input type="password" autocomplete="off" bind:value={form.ntfy.topic} maxlength="64" placeholder={view.ntfy.has_topic ? t('alerts.channels.saved') : ''} />
          </label>
          <label>
            {t('alerts.channels.token')}
            <input type="password" autocomplete="off" bind:value={form.ntfy.token} maxlength="256" placeholder={view.ntfy.has_token ? t('alerts.channels.saved') : ''} />
          </label>
          {#if view.ntfy.has_token}
            <label class="check"><input type="checkbox" bind:checked={form.ntfy.clear_token} /> {t('alerts.channels.clearToken')}</label>
          {/if}
          <button type="button" disabled={busy || !form.ntfy.server} onclick={() => test('ntfy')}>{t('alerts.channels.test')}</button>
        </fieldset>
        <fieldset>
          <legend>{t('alerts.channels.smtp')}</legend>
          <label class="check"><input type="checkbox" bind:checked={form.smtp.enabled} /> {t('alerts.channels.enabled')}</label>
          <label>{t('alerts.channels.host')} <input type="text" bind:value={form.smtp.host} maxlength="253" /></label>
          <div class="row">
            <label>
              {t('alerts.channels.security')}
              <select bind:value={form.smtp.security}>
                <option value="starttls">{t('alerts.channels.starttls')}</option>
                <option value="tls">{t('alerts.channels.tls')}</option>
              </select>
            </label>
            <label>{t('alerts.channels.port')} <input type="number" bind:value={form.smtp.port} min="1" max="65535" /></label>
          </div>
          <label>{t('alerts.channels.username')} <input type="text" autocomplete="off" bind:value={form.smtp.username} maxlength="254" /></label>
          {#if form.smtp.username}
            <label>
              {t('alerts.channels.password')}
              <input type="password" autocomplete="off" bind:value={form.smtp.password} maxlength="512" placeholder={view.smtp.has_password ? t('alerts.channels.saved') : ''} />
            </label>
          {/if}
          <label>{t('alerts.channels.from')} <input type="email" bind:value={form.smtp.from} maxlength="254" /></label>
          <label>{t('alerts.channels.to')} <input type="email" bind:value={form.smtp.to} maxlength="254" /></label>
          <button type="button" disabled={busy || !form.smtp.host} onclick={() => test('smtp')}>{t('alerts.channels.test')}</button>
        </fieldset>
      </div>
      <button type="submit" class="primary" disabled={busy}>{t('alerts.channels.save')}</button>
    </form>
  </section>
{/if}

<style>
  section {
    margin-top: var(--space-5);
  }
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: 0 0 var(--space-1);
  }
  .hint {
    color: var(--text-muted);
    font-size: 13px;
    margin: 0 0 var(--space-3);
  }
  form {
    display: grid;
    gap: var(--space-3);
    justify-items: start;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: var(--space-3);
    width: 100%;
  }
  fieldset {
    display: grid;
    align-content: start;
    gap: var(--space-2);
    min-width: 0;
    margin: 0;
    padding: var(--space-3) var(--space-4);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
  }
  legend {
    font-weight: 600;
    padding: 0 var(--space-1);
  }
  label {
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    color: var(--text-muted);
    min-width: 0;
  }
  label.check {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text);
  }
  .row {
    display: grid;
    grid-template-columns: 1fr 90px;
    gap: var(--space-2);
  }
  input:not([type='checkbox']),
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
    min-width: 0;
    width: 100%;
    box-sizing: border-box;
  }
  button {
    justify-self: start;
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  button.primary {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
</style>

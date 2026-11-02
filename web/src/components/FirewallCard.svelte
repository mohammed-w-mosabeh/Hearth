<script lang="ts">
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Params, type Plan } from '../lib/actions';
  import { api, ApiError } from '../lib/api';
  import { emptyRule, ruleParams, sourceLooksValid, validPort, type FirewallRule, type FirewallState } from '../lib/firewall';
  import { t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  // ufw on the Network page (ROADMAP 7.2). Every change goes through a
  // preview; turning the firewall off is typed out. Only plain port rules
  // can be changed here: the rest, and nftables, are for reading.
  interface Pending {
    action: string;
    params: Params;
    plan: Plan;
    title: string;
    verb: string;
  }

  let fw = $state.raw<FirewallState | null>(null);
  let failed = $state(false);
  let form = $state(emptyRule());
  let pending = $state.raw<Pending | null>(null);

  async function load(): Promise<void> {
    try {
      fw = await api<FirewallState>('/firewall');
      failed = false;
    } catch {
      failed = true;
    }
  }

  async function ask(action: string, params: Params, title: string, verb: string): Promise<void> {
    try {
      const plan = await preview('firewall', action, 'ufw', params);
      const first = plan.blocked[0];
      if (plan.items.length === 0 && first) {
        toast(t(reasonKey(first.reason), { detail: first.detail ?? '', user: '' }), { tone: 'error' });
        await load();
        return;
      }
      pending = { action, params, plan, title, verb };
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    const c = pending;
    pending = null;
    if (!c) return;
    try {
      const res = await run('firewall', c.action, 'ufw', c.params, c.plan.token, typed);
      const first = res.failed[0];
      if (first) {
        toast(t(reasonKey(first.reason), { detail: first.detail ?? '', user: '' }), { tone: 'error' });
      } else {
        toast(t('fw.done'));
        form = emptyRule();
      }
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    }
    await load();
  }

  $effect(() => {
    void load();
  });

  const ready = $derived(validPort(form.port) && sourceLooksValid(form.source));
  const family = (f: FirewallRule['family']) => (f === 'both' ? t('fw.both') : f === 'ipv4' ? 'IPv4' : 'IPv6');
</script>

{#if failed}
  <section class="card" aria-labelledby="fw-title">
    <h2 id="fw-title">{t('fw.title')}</h2>
    <p class="muted" role="status">{t('fw.error')}</p>
  </section>
{:else if fw?.installed}
  <section class="card" aria-labelledby="fw-title">
    <div class="head">
      <h2 id="fw-title">{t('fw.title')}</h2>
      {#if fw.active}
        <button type="button" class="btn" onclick={() => ask('disable', {}, t('fw.disable.title'), t('fw.disable.verb'))}>{t('fw.disable')}</button>
      {:else}
        <button type="button" class="btn" onclick={() => ask('enable', {}, t('fw.enable.title'), t('fw.enable.verb'))}>{t('fw.enable')}</button>
      {/if}
    </div>
    <p class="muted">
      {fw.active ? t('fw.on', { policy: fw.incoming ?? '' }) : t('fw.off')}
    </p>

    {#if fw.rules.length > 0}
      <div class="scroll">
        <table>
          <thead>
            <tr><th>{t('fw.col.action')}</th><th>{t('fw.col.port')}</th><th>{t('fw.col.source')}</th><th>{t('fw.col.family')}</th><th></th></tr>
          </thead>
          <tbody>
            {#each fw.rules as r, i (i)}
              <tr>
                {#if r.editable}
                  <td>{r.action}</td>
                  <td>{r.port}/{r.proto}</td>
                  <td>{r.source || t('fw.anywhere')}</td>
                  <td>{family(r.family)}</td>
                  <td>
                    {#if fw.active}
                      <button
                        type="button"
                        class="btn small"
                        aria-label={t('fw.delete.label', { port: r.port ?? 0, proto: r.proto })}
                        onclick={() => ask('rule-delete', ruleParams(r), t('fw.delete.title'), t('fw.delete.verb'))}>{t('fw.delete')}</button>
                    {/if}
                  </td>
                {:else}
                  <td colspan="3"><code>{r.text}</code></td>
                  <td>{family(r.family)}</td>
                  <td class="muted">{t('fw.readOnly')}</td>
                {/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if fw.truncated}<p class="muted">{t('fw.truncated')}</p>{/if}
    {:else}
      <p class="muted">{t('fw.noRules')}</p>
    {/if}

    {#if fw.active}
      <form
        class="add"
        onsubmit={(e) => {
          e.preventDefault();
          if (ready) void ask('rule-add', ruleParams(form), t('fw.add.title'), t('fw.add.verb'));
        }}
      >
        <label>
          {t('fw.col.action')}
          <select bind:value={form.action}>
            <option value="allow">{t('fw.allow')}</option>
            <option value="deny">{t('fw.deny')}</option>
          </select>
        </label>
        <label>
          {t('fw.proto')}
          <select bind:value={form.proto}>
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
          </select>
        </label>
        <label>
          {t('fw.col.port')}
          <input type="number" min="1" max="65535" step="1" bind:value={form.port} />
        </label>
        <label>
          {t('fw.col.source')}
          <input type="text" bind:value={form.source} placeholder={t('fw.anywhere')} aria-invalid={!sourceLooksValid(form.source)} />
        </label>
        <button type="submit" class="btn" disabled={!ready}>{t('fw.add')}</button>
      </form>
    {/if}
  </section>

  <ConfirmDialog
    plan={pending?.plan ?? null}
    title={pending?.title ?? ''}
    verb={pending?.verb ?? ''}
    body={pending?.plan.items[0]?.name ?? ''}
    warning={pending?.plan.items[0]?.detail ?? ''}
    onconfirm={confirmed}
    oncancel={() => (pending = null)}
  />
{/if}

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-top: var(--space-4);
    min-width: 0;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-3);
  }
  h2 {
    font-size: 15px;
    margin: 0;
  }
  .muted {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .scroll {
    overflow-x: auto;
    margin-top: var(--space-2);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th,
  td {
    text-align: start;
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border);
  }
  th {
    color: var(--text-muted);
    font-weight: 600;
  }
  code {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .add {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: flex-end;
    margin-top: var(--space-3);
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
    color: var(--text-muted);
  }
  input,
  select,
  .btn {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  input[type='number'] {
    width: 7em;
  }
  .btn {
    cursor: pointer;
    white-space: nowrap;
  }
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .btn.small {
    font-size: 12px;
    padding: 0 var(--space-2);
  }
</style>

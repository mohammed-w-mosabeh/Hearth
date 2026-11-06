<script lang="ts">
  import ConfirmDialog from './ConfirmDialog.svelte';
  import LogExplorer from './LogExplorer.svelte';
  import { api } from '../lib/api';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { failure } from '../lib/errors';
  import { t, type MessageKey } from '../lib/i18n';
  import { openInspect } from '../lib/router.svelte';
  import { addresses, exposure, logSources, restartPolicies, serviceCount, shape, shapes, storage, type StackInfo } from '../lib/stacks';
  import { toast } from '../lib/toast.svelte';

  // One managed app (ROADMAP 9.9): what runs, what it exposes, where its
  // data lives, its secrets by name, its logs, and its actions. Every
  // action goes through the actions endpoint; the broker asks for the
  // password. A secret's value shows only after Reveal, which asks every
  // time, and goes away with Hide or when the dialog closes.
  let { stack, onchanged }: { stack: StackInfo; onchanged: () => void } = $props();

  type Op = 'start' | 'stop' | 'restart';
  const opLabel: Record<Op, MessageKey> = { start: 'docker.op.start', stop: 'docker.op.stop', restart: 'docker.op.restart' };
  const shapeLabel = { ok: 'stacks.state.ok', degraded: 'stacks.state.degraded', warning: 'stacks.state.warning', stopped: 'stacks.state.stopped' } as const;

  let busy = $state(false);
  let revealed = $state.raw<Record<string, string>>({});
  let confirming = $state.raw<{ action: 'update' | 'remove'; plan: Plan; params: Record<string, string> } | null>(null);
  let removeVolumes = $state(false);

  const sh = $derived(shape(stack));
  const ex = $derived(exposure(stack));

  function report(res: Result, done: string): void {
    const bad = [...res.blocked, ...res.failed];
    const first = bad[0];
    if (!first) {
      toast(done);
      return;
    }
    toast(t('stacks.failed', { name: stack.name, reason: first.detail || t(reasonKey(first.reason), { detail: '', user: '' }) }), { tone: 'error' });
  }

  async function act(op: Op): Promise<void> {
    busy = true;
    toast(t('docker.authHint'));
    try {
      report(await run('stack', op, stack.name), t('stacks.done', { name: stack.name }));
      onchanged();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }

  async function ask(action: 'update' | 'remove'): Promise<void> {
    busy = true;
    const params: Record<string, string> = action === 'remove' ? { volumes: removeVolumes ? 'true' : 'false' } : {};
    try {
      const plan = await preview('stack', action, stack.name, params);
      if (action === 'update' && plan.items.every((i) => !i.detail?.includes('new digest'))) {
        toast(t('stacks.upToDate', { name: stack.name }));
        return;
      }
      confirming = { action, plan, params };
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }

  async function confirm(typed: string): Promise<void> {
    if (!confirming) return;
    const { action, plan, params } = confirming;
    confirming = null;
    busy = true;
    toast(t('docker.authHint'));
    try {
      report(await run('stack', action, stack.name, params, plan.token, typed), t('stacks.done', { name: stack.name }));
      onchanged();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }

  async function reveal(key: string): Promise<void> {
    toast(t('docker.authHint'));
    try {
      const r = await api<{ value: string }>(`/stacks/${encodeURIComponent(stack.name)}/secrets/${encodeURIComponent(key)}/reveal`, { method: 'POST' });
      revealed = { ...revealed, [key]: r.value };
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  function hide(key: string): void {
    revealed = Object.fromEntries(Object.entries(revealed).filter(([k]) => k !== key));
  }
</script>

<dl class="facts">
  <dt>{t('stacks.col.state')}</dt>
  <dd class="tone-{sh}"><span aria-hidden="true">{shapes[sh]}</span> {t(shapeLabel[sh])} · {t('stacks.servicesOf', { n: serviceCount(stack) })}</dd>
  <dt>{t('stacks.col.address')}</dt>
  <dd class="mono">{addresses(stack).join(', ') || '–'}</dd>
  <dt>{t('stacks.restart')}</dt>
  <dd class="mono">{restartPolicies(stack).join(', ') || '–'}</dd>
</dl>

{#each ex.danger as f (f)}
  <p class="danger" role="note"><span aria-hidden="true">▲</span> {t('stacks.dangerFlag', { flag: f })}</p>
{/each}
{#each ex.exposed as a (a)}
  <p class="danger" role="note"><span aria-hidden="true">▲</span> {t('stacks.exposed', { address: a })}</p>
{/each}

<section>
  <h3>{t('stacks.containers')}</h3>
  <ul class="list">
    {#each stack.containers as c (c.id)}
      <li>
        <button type="button" class="link" onclick={() => openInspect(`container:${c.id}`)}>{c.name}</button>
        <span class="muted">{c.service} · {c.state}{#if c.health} · {c.health}{/if}{#if c.restart_count > 0} · {t('stacks.restarts', { n: c.restart_count })}{/if}</span>
      </li>
    {:else}
      <li class="muted">{t('stacks.noContainers')}</li>
    {/each}
  </ul>
</section>

<section>
  <h3>{t('stacks.storage')}</h3>
  <ul class="list">
    {#each storage(stack) as v (v.kind + v.name)}
      <li><span class="mono">{v.name}</span> <span class="muted">· {t(v.kind === 'bind' ? 'stacks.bind' : 'stacks.volume')}</span></li>
    {:else}
      <li class="muted">{t('stacks.noStorage')}</li>
    {/each}
  </ul>
</section>

<section>
  <h3>{t('stacks.secrets')}</h3>
  <ul class="list">
    {#each stack.secrets as s (s.key)}
      <li class="secret">
        <span class="mono">{s.key}</span>
        {#if !s.set}
          <span class="warn">{t('stacks.secretUnset')}</span>
        {:else if s.key in revealed}
          <code class="value">{revealed[s.key]}</code>
          <button type="button" class="plain" onclick={() => hide(s.key)}>{t('stacks.hide')}</button>
        {:else}
          <span class="muted" aria-label={t('stacks.hidden')}>••••••••</span>
          <button type="button" class="plain" onclick={() => reveal(s.key)}>{t('stacks.reveal')}</button>
        {/if}
      </li>
    {:else}
      <li class="muted">{t('stacks.noSecrets')}</li>
    {/each}
  </ul>
</section>

<section>
  <h3>{t('stacks.logs')}</h3>
  <LogExplorer initial={logSources(stack)} />
</section>

<div class="actions">
  {#each ['start', 'stop', 'restart'] as const as op (op)}
    <button type="button" class="plain" disabled={busy} onclick={() => act(op)}>{t(opLabel[op])}</button>
  {/each}
  <button type="button" class="plain" disabled={busy} onclick={() => ask('update')}>{t('stacks.update')}</button>
  <span class="spacer"></span>
  <label class="check"><input type="checkbox" bind:checked={removeVolumes} /> {t('stacks.removeVolumes')}</label>
  <button type="button" class="plain danger-btn" disabled={busy} onclick={() => ask('remove')}>{t('stacks.remove')}</button>
</div>

<ConfirmDialog
  plan={confirming?.plan ?? null}
  title={t(confirming?.action === 'remove' ? 'stacks.removeTitle' : 'stacks.updateTitle', { name: stack.name })}
  verb={t(confirming?.action === 'remove' ? 'stacks.remove' : 'stacks.update')}
  body={confirming?.action === 'remove' ? t(removeVolumes ? 'stacks.removeVolumesBody' : 'stacks.removeBody') : t('stacks.updateBody')}
  onconfirm={confirm}
  oncancel={() => (confirming = null)}
/>

<style>
  .facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--space-1) var(--space-4);
    margin: 0 0 var(--space-3);
    font-size: 13px;
  }
  dt {
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  section {
    margin-top: var(--space-4);
  }
  h3 {
    font-size: 13px;
    margin: 0 0 var(--space-2);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
  }
  .secret {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
  .value {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
    background: var(--surface-2, var(--surface));
    padding: 0 var(--space-1);
  }
  .danger {
    margin: var(--space-2) 0 0;
    padding: var(--space-2) var(--space-3);
    border: 1px solid var(--crit);
    border-radius: var(--radius-s);
    font-size: 13px;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .muted {
    color: var(--text-muted);
  }
  .warn,
  .tone-warning,
  .tone-degraded {
    color: var(--warn-text, var(--text));
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-4);
  }
  .spacer {
    flex: 1;
  }
  .check {
    font-size: 13px;
    display: inline-flex;
    gap: var(--space-1);
    align-items: center;
  }
  .plain {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 2px var(--space-3);
    cursor: pointer;
  }
  .plain:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .danger-btn {
    border-color: var(--crit);
  }
  .link {
    font: inherit;
    font-weight: 500;
    color: var(--text);
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .link:hover,
  .link:focus-visible {
    text-decoration: underline;
  }
</style>

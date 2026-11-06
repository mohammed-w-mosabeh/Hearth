<script lang="ts">
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { api } from '../lib/api';
  import { preview, reasonKey, run, type Plan } from '../lib/actions';
  import { failure } from '../lib/errors';
  import { t } from '../lib/i18n';
  import { dryRunQuery, type DryRun } from '../lib/stacks';
  import { toast } from '../lib/toast.svelte';

  // The import dry-run (ROADMAP 9.7) and the confirmed import (9.8). The
  // dry-run changes nothing; no secret value reaches this page, only names.
  // Marking a name plain or leaving a service out runs it again; Import
  // previews stack/import and needs the project's name typed back.
  let { project, onimported, onclose }: { project: string; onimported: () => void; onclose: () => void } = $props();

  let result = $state.raw<DryRun | null>(null);
  let error = $state('');
  let loading = $state(false);
  let plainText = $state('');
  let skip = $state.raw<string[]>([]);
  let confirming = $state.raw<Plan | null>(null);

  const plain = $derived(
    plainText
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s !== ''),
  );
  const services = $derived([...new Set((result?.containers ?? []).map((c) => c.service))].sort());

  async function check(): Promise<void> {
    loading = true;
    try {
      result = await api<DryRun>(`/stacks/import/${encodeURIComponent(project)}${dryRunQuery(plain, skip)}`);
      error = '';
    } catch (err) {
      error = failure(err, 'stacks.importError');
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void project;
    void check();
  });

  function toggleSkip(svc: string): void {
    skip = skip.includes(svc) ? skip.filter((s) => s !== svc) : [...skip, svc].sort();
  }

  function params(): Record<string, string> {
    const p: Record<string, string> = {};
    if (plain.length) p.plain = plain.join(',');
    if (skip.length) p.skip = skip.join(',');
    return p;
  }

  async function ask(): Promise<void> {
    try {
      const plan = await preview('stack', 'import', project, params());
      if (plan.blocked.length > 0) {
        toast(t('stacks.importBlocked', { name: project }), { tone: 'error' });
        await check();
        return;
      }
      confirming = plan;
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function confirm(typed: string): Promise<void> {
    if (!confirming) return;
    const plan = confirming;
    confirming = null;
    toast(t('docker.authHint'));
    try {
      const res = await run('stack', 'import', project, params(), plan.token, typed);
      const first = [...res.blocked, ...res.failed][0];
      if (first) {
        toast(t('stacks.failed', { name: project, reason: first.detail || t(reasonKey(first.reason), { detail: '', user: '' }) }), { tone: 'error' });
        return;
      }
      toast(t('stacks.imported', { name: project }));
      onimported();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }
</script>

<div class="head">
  <h2>{t('stacks.importTitle', { name: project })}</h2>
  <span class="chip">{t('stacks.nothingChanged')}</span>
  <span class="spacer"></span>
  <button type="button" class="plain" onclick={onclose}>{t('stacks.close')}</button>
</div>

{#if error}
  <p class="error" role="alert">{error}</p>
{:else if result === null}
  <p class="muted">{t('stacks.checking')}</p>
{:else}
  <div class="grid">
    <section class="card">
      <h3>{t('stacks.refused')}</h3>
      <ul class="list">
        {#each result.refused as r (r)}<li class="crit"><span aria-hidden="true">■</span> {r}</li>{:else}<li class="muted">{t('stacks.nothingRefused')}</li>{/each}
      </ul>
    </section>
    <section class="card">
      <h3>{t('stacks.changes')}</h3>
      <ul class="list">
        {#each result.changes as c (c)}<li><span aria-hidden="true">▲</span> {c}</li>{:else}<li class="muted">{t('stacks.noChanges')}</li>{/each}
        {#each result.flags as f (f)}<li class="crit"><span aria-hidden="true">▲</span> {t('stacks.dangerFlag', { flag: f })}</li>{/each}
      </ul>
    </section>
    <section class="card">
      <h3>{t('stacks.dataStays')}</h3>
      <ul class="list">
        {#each result.volumes as v (v.name)}
          <li><span class="mono">{v.name}</span> · {t(v.exists ? 'stacks.reused' : 'stacks.createdEmpty')}</li>
        {/each}
        {#each result.spec.bind_roots ?? [] as b (b)}<li><span class="mono">{b}</span> · {t('stacks.bindInPlace')}</li>{/each}
        {#each result.ports as p (p.service + p.host + p.protocol)}
          <li>
            <span class="mono">{p.host_ip}:{p.host}/{p.protocol}</span> · {p.service}{#if p.conflict}<span class="crit"> · {t('stacks.portHeld', { by: p.conflict })}</span>{/if}
          </li>
        {/each}
      </ul>
      {#each result.notes as n (n)}<p class="muted note">{n}</p>{/each}
    </section>
    <section class="card">
      <h3>{t('stacks.secrets')}</h3>
      <p class="muted note">{t('stacks.secretsMoved', { n: result.secrets.length })}</p>
      <ul class="list mono">
        {#each result.secrets as s (s.key)}<li>{s.key} <span class="muted">· {s.services.join(', ')}</span></li>{/each}
      </ul>
    </section>
  </div>

  <section class="card choices">
    <label>
      {t('stacks.markPlain')}
      <input type="text" bind:value={plainText} placeholder="TZ, discovery.type" />
    </label>
    {#if services.length > 0}
      <fieldset>
        <legend>{t('stacks.leaveOut')}</legend>
        {#each services as svc (svc)}
          <label class="check"><input type="checkbox" checked={skip.includes(svc)} onchange={() => toggleSkip(svc)} /> {svc}</label>
        {/each}
      </fieldset>
    {/if}
    <div class="row">
      <button type="button" class="plain" disabled={loading} onclick={check}>{t('stacks.checkAgain')}</button>
      <button type="button" class="plain primary" disabled={loading || result.refused.length > 0} onclick={ask}>{t('stacks.import')}</button>
    </div>
  </section>
{/if}

<ConfirmDialog
  plan={confirming}
  title={t('stacks.importConfirmTitle', { name: project })}
  verb={t('stacks.import')}
  body={t('stacks.importBody')}
  onconfirm={confirm}
  oncancel={() => (confirming = null)}
/>

<style>
  .head {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    flex-wrap: wrap;
    margin-bottom: var(--space-3);
  }
  h2 {
    font-size: 16px;
    margin: 0;
  }
  h3 {
    font-size: 13px;
    margin: 0 0 var(--space-2);
  }
  .spacer {
    flex: 1;
  }
  .chip {
    font-size: 12px;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 var(--space-2);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: var(--space-3);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    font-size: 13px;
    min-width: 0;
  }
  .choices {
    margin-top: var(--space-3);
    display: grid;
    gap: var(--space-3);
  }
  .choices input[type='text'] {
    font: inherit;
    width: 100%;
    margin-top: var(--space-1);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
  }
  fieldset {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
  }
  legend {
    padding: 0;
    margin-bottom: var(--space-1);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--space-1);
    overflow-wrap: anywhere;
  }
  .note {
    font-size: 12px;
    margin: var(--space-2) 0 0;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .muted {
    color: var(--text-muted);
  }
  .crit {
    font-weight: 600;
  }
  .error {
    color: var(--text);
    border: 1px solid var(--crit);
    border-radius: var(--radius-s);
    padding: var(--space-2) var(--space-3);
  }
  .row,
  .check {
    display: flex;
    gap: var(--space-2);
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
  .plain.primary {
    border-color: var(--accent);
  }
  .plain:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>

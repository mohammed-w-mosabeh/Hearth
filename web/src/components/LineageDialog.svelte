<script lang="ts">
  import Skeleton from './Skeleton.svelte';
  import { failure } from '../lib/errors';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { api } from '../lib/api';
  import { imageName, type Container, type Snapshot } from '../lib/docker';
  import { t, type MessageKey } from '../lib/i18n';
  import { browserURL, containerOf, lineageQuery, ownerName, unitScope, type Lineage, type PkgRef, type Row } from '../lib/network';
  import { href } from '../lib/router.svelte';
  import { toast } from '../lib/toast.svelte';

  // "What is this?" (SPEC 11): a listening port, the process behind it,
  // its systemd unit, the package that installed them and the container
  // it serves. Everything shown is system-derived text, escaped by Svelte.
  let { row, onclose, onchanged }: { row: Row | null; onclose: () => void; onchanged: () => void } = $props();

  let dialog: HTMLDialogElement;
  let lineage = $state.raw<Lineage | null>(null);
  let loading = $state(false);
  let failed = $state('');
  let container = $state.raw<Container | null>(null);

  $effect(() => {
    if (!row) {
      if (dialog.open) dialog.close();
      return;
    }
    lineage = null;
    container = null;
    failed = '';
    if (!dialog.open) dialog.showModal();
    let stopped = false;
    const q = lineageQuery(row.owner);
    if (q) {
      loading = true;
      api<Lineage>(`/net/lineage?${q}`)
        .then((l) => {
          if (!stopped) lineage = l;
        })
        .catch((err: unknown) => {
          if (!stopped) failed = failure(err, 'net.lineage.error');
        })
        .finally(() => {
          if (!stopped) loading = false;
        });
    }
    // Docker is asked (through hearth-broker) only when the port is a
    // container's: its own process, or Docker's proxy for a published port.
    const r = row;
    if (r.owner?.container || r.owner?.unit === 'docker.service') {
      api<Snapshot>('/docker')
        .then((s) => {
          if (!stopped && s.state === 'ok') container = containerOf(r, s.containers);
        })
        .catch(() => {});
    }
    return () => {
      stopped = true;
    };
  });

  const url = $derived(row ? browserURL(row) : null);
  // The program's package first, then the unit file's when it differs.
  const packages = $derived.by(() => {
    const out: PkgRef[] = [];
    for (const p of [lineage?.exe_package, lineage?.unit_package]) if (p && !out.some((q) => q.id === p.id)) out.push(p);
    return out;
  });
  const scope = $derived(unitScope(row?.owner));
  // The unit worth acting on: a service, not an app's scope or a container's.
  const unit = $derived(row?.owner?.unit && (row.owner.kind === 'service' || row.owner.kind === 'user-service') ? row.owner.unit : null);
  const scopeKey: Record<string, MessageKey> = { all: 'net.scope.all', address: 'net.scope.address', loopback: 'net.scope.loopback' };

  type Op = { kind: 'unit' | 'container' | 'proc'; action: string; target: string; name: string; done: MessageKey };
  let pending = $state<{ op: Op; plan: Plan } | null>(null);

  function report(op: Op, res: Result): void {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done === 0 && bad) {
      const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: bad.detail ?? '' });
      toast(t('net.failed', { name: op.name, reason }), { tone: 'error' });
      return;
    }
    toast(t(op.done, { name: op.name }));
    onchanged();
  }

  async function act(op: Op): Promise<void> {
    try {
      const plan = await preview(op.kind, op.action, op.target);
      if (plan.items.length === 0) {
        report(op, { done: 0, failed: [], blocked: plan.blocked });
        return;
      }
      if (plan.risk === 'destructive') {
        pending = { op, plan };
        return;
      }
      if (op.kind === 'unit' && scope === 'system') toast(t('svc.authHint'));
      report(op, await run(op.kind, op.action, op.target, {}, plan.token));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { op, plan } = pending;
    pending = null;
    try {
      report(op, await run(op.kind, op.action, op.target, {}, plan.token, typed || undefined));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    }
  }

  function stopUnit(): void {
    if (unit && scope) void act({ kind: 'unit', action: 'stop', target: `${scope}/${unit}`, name: unit, done: 'net.done.stopUnit' });
  }
  function stopContainer(c: Container): void {
    void act({ kind: 'container', action: 'stop', target: c.id, name: c.name, done: 'net.done.stopContainer' });
  }
  function endProcess(): void {
    const o = row?.owner;
    if (o?.pid && o.start) void act({ kind: 'proc', action: 'terminate', target: `pid:${o.pid}:${o.start}`, name: o.name ?? String(o.pid), done: 'net.done.endProcess' });
  }
</script>

<dialog bind:this={dialog} aria-labelledby="lineage-title" onclose={() => row && onclose()}>
  {#if row}
    {@const o = row.owner}
    <header>
      <h2 id="lineage-title">{t('net.lineage.title', { proto: row.proto.toUpperCase(), port: row.port })}</h2>
      <button type="button" class="x" aria-label={t('net.lineage.close')} onclick={() => onclose()}>×</button>
    </header>

    <ol class="chain">
      <li>
        <span class="step">{t('net.lineage.port')}</span>
        <div>
          <strong>{row.proto.toUpperCase()} {row.port}</strong>
          <span class="muted">{t('net.lineage.on', { addrs: row.addresses.join(', ') })}</span>
          <p class:warn={row.scope === 'all'}>{t(scopeKey[row.scope] ?? 'net.scope.address')}</p>
        </div>
      </li>

      <li>
        <span class="step">{t('net.lineage.process')}</span>
        <div>
          {#if o?.pid}
            <strong>{o.name || '?'}</strong> <span class="muted">{t('net.lineage.pid', { pid: o.pid })}</span>
            <p class="muted">{o.exact ? t('net.lineage.exact') : t('net.lineage.inexact')}</p>
          {:else}
            <p class="muted">{t('net.lineage.noProcess', { user: row.user })}</p>
          {/if}
          {#if lineage?.exe}
            <p class="mono">{lineage.exe}{#if !lineage.exe_exact}<span class="muted"> {t('net.lineage.fromUnit')}</span>{/if}</p>
          {/if}
        </div>
      </li>

      {#if o?.unit}
        <li>
          <span class="step">{o.kind === 'container' ? t('net.lineage.scope') : t('net.lineage.unit')}</span>
          <div>
            <strong>{o.unit}</strong>
            {#if lineage?.unit}
              {#if lineage.unit.description}<span class="muted">{lineage.unit.description}</span>{/if}
              <p class="muted">
                {lineage.unit.active ?? ''}{lineage.unit.sub ? ` (${lineage.unit.sub})` : ''} · {t(scope === 'user' ? 'net.lineage.userUnit' : 'net.lineage.systemUnit')}
              </p>
              {#if lineage.unit.file}<p class="mono">{lineage.unit.file}</p>{/if}
            {/if}
            {#if unit}<p><a href={href('services', unit)} onclick={() => onclose()}>{t('net.lineage.openServices')}</a></p>{/if}
          </div>
        </li>
      {/if}

      <li>
        <span class="step">{t('net.lineage.package')}</span>
        <div>
          {#if loading}
            <Skeleton label={t('net.lineage.loading')} rows={2} />
          {:else if failed}
            <p class="error">{failed}</p>
          {:else if packages.length > 0}
            {#each packages as p (p.id)}
              <p>
                <a href={href('packages', p.name)} onclick={() => onclose()}><strong>{p.name}</strong></a>
                {#if p.version}<span class="muted">{p.version}</span>{/if}
              </p>
            {/each}
          {:else}
            <p class="muted">{t('net.lineage.noPackage')}</p>
          {/if}
        </div>
      </li>

      {#if container}
        <li>
          <span class="step">{t('net.lineage.container')}</span>
          <div>
            <a href={href('docker', container.name)} onclick={() => onclose()}><strong>{container.name}</strong></a>
            <span class="muted">{imageName(container)} · {container.status}</span>
          </div>
        </li>
      {/if}
    </ol>

    <div class="actions">
      {#if url}<a class="btn" href={url} target="_blank" rel="noopener noreferrer">{t('net.op.open')}</a>{/if}
      {#if unit}<button type="button" class="btn" onclick={stopUnit}>{t('net.op.stopUnit', { name: unit })}</button>{/if}
      {#if container}{@const c = container}<button type="button" class="btn" onclick={() => stopContainer(c)}>{t('net.op.stopContainer', { name: c.name })}</button>{/if}
      {#if o?.exact && o.pid}<button type="button" class="btn danger" onclick={endProcess}>{t('net.op.endProcess', { name: ownerName(o) || (o.name ?? '') })}</button>{/if}
    </div>
  {/if}
</dialog>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('net.confirmTitle', { name: pending.op.name }) : ''}
  verb={pending?.op.kind === 'proc' ? t('net.op.end') : t('net.op.stop')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<style>
  dialog {
    width: min(640px, calc(100vw - 32px));
    max-height: min(760px, calc(100vh - 32px));
    padding: var(--space-4);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h2 {
    font-size: 17px;
  }
  .x {
    font: inherit;
    font-size: 20px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 0;
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
  }
  .chain {
    list-style: none;
    padding: 0;
    margin: var(--space-3) 0 0;
    font-size: 13px;
  }
  .chain li {
    display: grid;
    grid-template-columns: 88px 1fr;
    gap: var(--space-3);
    padding: var(--space-2) 0;
    border-inline-start: 2px solid var(--border);
    padding-inline-start: var(--space-3);
    position: relative;
  }
  .chain li::before {
    content: '';
    position: absolute;
    inset-inline-start: -5px;
    top: 14px;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--accent);
  }
  .step {
    color: var(--text-muted);
    font-size: 12px;
    padding-top: 1px;
  }
  .chain p {
    margin: 2px 0 0;
    overflow-wrap: anywhere;
  }
  .chain strong {
    font-weight: 600;
    margin-inline-end: var(--space-2);
  }
  .muted {
    color: var(--text-muted);
  }
  .warn {
    color: var(--warn);
  }
  .error {
    color: var(--crit);
  }
  .mono {
    font-family: var(--font-mono, monospace);
    font-size: 12px;
  }
  a {
    color: var(--text);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-4);
  }
  .btn {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    text-decoration: none;
  }
  .btn.danger {
    color: var(--crit);
  }
</style>

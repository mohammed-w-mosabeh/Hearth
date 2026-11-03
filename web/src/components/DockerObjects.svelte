<script lang="ts">
  import { failure } from '../lib/errors';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Plan } from '../lib/actions';
  import { api } from '../lib/api';
  import { networkRemovable, usedByNames, type Container, type Network, type Volume } from '../lib/docker';
  import { bytes } from '../lib/format';
  import { t } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  // Networks or volumes (ROADMAP 5.5). Removing goes through a preview
  // (SECURITY S15), and the server decides what counts as an orphan: this
  // page only hides the button where it would be refused.
  let {
    kind,
    networks,
    volumes,
    containers,
  }: { kind: 'network' | 'volume'; networks: Network[]; volumes: Volume[]; containers: Pick<Container, 'id' | 'name'>[] } = $props();

  interface Row {
    target: string;
    name: string;
    driver: string;
    project?: string | undefined;
    users: string[];
    removable: boolean;
  }

  const rows = $derived<Row[]>(
    kind === 'network'
      ? networks.map((n) => ({ target: n.id, name: n.name, driver: n.driver, project: n.project, users: usedByNames(n.containers, containers), removable: networkRemovable(n) }))
      : volumes.map((v) => ({ target: v.name, name: v.name, driver: v.driver, project: v.project, users: usedByNames(v.containers, containers), removable: v.containers.length === 0 })),
  );

  let sizes = $state.raw<Map<string, number> | null>(null);
  let pending = $state<{ row: Row; plan: Plan } | null>(null);
  let busy = $state('');

  const problem = (err: unknown) => (failure(err, 'action.failed'));

  async function loadSizes(): Promise<void> {
    try {
      const r = await api<{ sizes: { name: string; size: number }[] }>('/docker/volumes/sizes');
      sizes = new Map(r.sizes.map((s) => [s.name, s.size]));
    } catch (err) {
      toast(problem(err), { tone: 'error' });
    }
  }

  $effect(() => {
    if (kind === 'volume') void loadSizes();
  });

  async function review(row: Row): Promise<void> {
    busy = row.target;
    try {
      const plan = await preview(kind, 'remove', row.target);
      const blocked = plan.blocked[0];
      if (blocked) toast(t('docker.objects.failed', { name: row.name, reason: t(reasonKey(blocked.reason), { detail: '', user: '' }) }), { tone: 'error' });
      else pending = { row, plan };
    } catch (err) {
      toast(problem(err), { tone: 'error' });
    } finally {
      busy = '';
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { row, plan } = pending;
    pending = null;
    busy = row.target;
    toast(t('docker.authHint'));
    try {
      const res = await run(kind, 'remove', row.target, {}, plan.token, typed);
      const bad = res.failed[0];
      if (bad) {
        const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
        toast(t('docker.objects.failed', { name: row.name, reason }), { tone: 'error' });
      } else {
        toast(t('docker.objects.done', { name: row.name }));
      }
    } catch (err) {
      toast(problem(err), { tone: 'error' });
    } finally {
      busy = '';
      if (kind === 'volume') await loadSizes();
    }
  }
</script>

{#if rows.length === 0}
  <p class="note">{t(kind === 'network' ? 'docker.objects.noNetworks' : 'docker.objects.noVolumes')}</p>
{:else}
  <div class="table">
    <table>
      <thead>
        <tr>
          <th>{t('docker.objects.name')}</th>
          <th>{t('docker.objects.driver')}</th>
          <th>{t('docker.objects.project')}</th>
          <th>{t('docker.objects.usedBy')}</th>
          {#if kind === 'volume'}<th class="num">{t('docker.objects.size')}</th>{/if}
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.target)}
          {@const size = sizes?.get(r.name)}
          <tr>
            <td class="name" title={r.name}>{r.name}</td>
            <td>{r.driver}</td>
            <td class="muted">{r.project ?? ''}</td>
            <td class="users" title={r.users.join(', ')}>
              {#if r.users.length > 0}{r.users.join(', ')}{:else if kind === 'network' && !r.removable}<span class="muted">{t('docker.objects.builtIn')}</span>{:else}<span class="muted">{t('docker.objects.unused')}</span>{/if}
            </td>
            {#if kind === 'volume'}<td class="num">{size !== undefined && size >= 0 ? bytes(size) : '–'}</td>{/if}
            <td class="end">
              {#if r.removable}
                <button type="button" disabled={busy !== ''} onclick={() => review(r)}>{t('docker.objects.remove')}</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="foot">{t(kind === 'network' ? 'docker.objects.footNetworks' : 'docker.objects.footVolumes')}</p>
{/if}

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('docker.objects.confirmTitle', { name: pending.row.name }) : ''}
  verb={t('docker.objects.remove')}
  body={t(kind === 'network' ? 'docker.objects.confirmNetwork' : 'docker.objects.confirmVolume')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<style>
  .note,
  .muted {
    color: var(--text-muted);
  }
  .table {
    overflow-x: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  th,
  td {
    padding: 0 var(--space-3);
    height: var(--row-h);
    text-align: start;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
  }
  tr:last-child td {
    border-bottom: 0;
  }
  th {
    color: var(--text-muted);
    font-weight: 600;
  }
  .num {
    text-align: end;
  }
  .end {
    text-align: end;
    width: 1%;
  }
  .name,
  .users {
    max-width: 240px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  button {
    font: inherit;
    font-size: 13px;
    color: var(--crit);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 2px var(--space-3);
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .foot {
    font-size: 12px;
    color: var(--text-muted);
    margin-top: var(--space-3);
  }
</style>

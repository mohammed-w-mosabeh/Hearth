<script lang="ts">
  import Skeleton from '../Skeleton.svelte';
  import { failure } from '../../lib/errors';
  import Act from './Act.svelte';
  import Chain from './Chain.svelte';
  import { api } from '../../lib/api';
  import { t, type MessageKey } from '../../lib/i18n';
  import type { Inspect, Link } from '../../lib/inspect';
  import { lineagePackages, lineageQuery, ownerName, rows, unitScope, type Lineage, type Row, type Sockets } from '../../lib/network';
  import { href } from '../../lib/router.svelte';

  // A listening port and what is behind it: the process, its unit, the
  // package that installed them and the container it serves.
  let { item }: { item: Extract<Inspect, { kind: 'port' }> } = $props();

  let found = $state.raw<Row[] | null>(null);
  let failed = $state('');
  let lineage = $state.raw<Lineage | null>(null);

  function reload(): void {
    api<Sockets>('/net/sockets')
      .then((s) => (found = rows(s.listeners).filter((r) => r.proto === item.proto && r.port === item.port)))
      .catch((err: unknown) => (failed = failure(err, 'inspect.error')));
  }

  $effect(() => reload());

  // Several owners can share a port (one per address): the first leads.
  const row = $derived(found?.[0] ?? null);
  $effect(() => {
    const q = lineageQuery(row?.owner);
    if (!q) return;
    let stopped = false;
    api<Lineage>(`/net/lineage?${q}`)
      .then((l) => {
        if (!stopped) lineage = l;
      })
      .catch(() => {});
    return () => {
      stopped = true;
    };
  });

  const o = $derived(row?.owner);
  const scope = $derived(unitScope(o));
  const service = $derived(o?.unit && (o.kind === 'service' || o.kind === 'user-service') ? o.unit : null);
  const scopeKey: Record<string, MessageKey> = { all: 'net.scope.all', address: 'net.scope.address', loopback: 'net.scope.loopback' };

  const links = $derived.by((): Link[] => {
    if (!row) return [];
    const out: Link[] = [{ label: `${item.proto} ${item.port}`, item: null }];
    if (o?.group) out.push({ label: ownerName(o) || o.group, item: { kind: 'proc', group: o.group } });
    if (o?.unit && scope && o.kind !== 'container') out.push({ label: o.unit, item: { kind: 'unit', scope, name: o.unit } });
    for (const p of lineagePackages(lineage)) out.push({ label: p.name, item: { kind: 'package', name: p.name } });
    if (o?.container) out.push({ label: o.container.slice(0, 12), item: { kind: 'container', id: o.container } });
    return out;
  });
</script>

<h2 id="inspect-title">{item.proto.toUpperCase()} {item.port}</h2>
<p class="sub">{t('inspect.kind.port')}</p>

{#if failed}
  <p class="error">{failed}</p>
{:else if found && !row}
  <p class="muted">{t('inspect.portClosed')}</p>
{:else if row}
  <dl>
    <dt>{t('inspect.addresses')}</dt>
    <dd class="mono">{row.addresses.join(', ')}</dd>
    <dt>{t('inspect.reach')}</dt>
    <dd>{t(scopeKey[row.scope] ?? 'net.scope.address')}</dd>
    <dt>{t('inspect.user')}</dt>
    <dd>{row.user}</dd>
    {#if o?.pid}<dt>{t('inspect.process')}</dt><dd>{o.name || '?'} <span class="mono muted">{o.pid}</span></dd>{/if}
    {#if lineage?.exe}<dt>{t('inspect.program')}</dt><dd class="mono">{lineage.exe}</dd>{/if}
    {#if (found?.length ?? 0) > 1}<dt>{t('inspect.others')}</dt><dd>{(found ?? []).slice(1).map((r) => ownerName(r.owner) || r.user).join(', ')}</dd>{/if}
  </dl>

  <h3>{t('inspect.actions')}</h3>
  <div class="actions">
    {#if service && scope}
      <Act kind="unit" action="stop" target={`${scope}/${service}`} name={service} label={t('svc.op.stop')} system={scope === 'system'} onchanged={() => setTimeout(reload, 1000)} />
    {:else if o?.container}
      <Act kind="container" action="stop" target={o.container} name={ownerName(o) || o.container} label={t('docker.op.stop')} onchanged={() => setTimeout(reload, 1000)} />
    {:else if o?.pid && o.start}
      <Act kind="proc" action="terminate" target={`pid:${o.pid}:${o.start}`} name={o.name ?? String(o.pid)} label={t('inspect.end')} onchanged={() => setTimeout(reload, 1000)} />
    {/if}
    <a href={href('network', String(item.port))}>{t('inspect.fullPage')}</a>
  </div>

  <Chain {links} />
{:else}
  <Skeleton label={t('inspect.loading')} rows={2} />
{/if}

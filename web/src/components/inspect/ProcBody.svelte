<script lang="ts">
  import Act from './Act.svelte';
  import Chain from './Chain.svelte';
  import MiniChart from './MiniChart.svelte';
  import { bytes, pct } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import { push, type Inspect, type Link } from '../../lib/inspect';
  import { href } from '../../lib/router.svelte';
  import type { ProcGroup, ProcSnapshot } from '../../lib/types';
  import type { Live } from '../../lib/ws';

  // A process group (an app, a unit or a container). Following "procs"
  // while the drawer is open is what makes hearthd scan /proc.
  let { item, live }: { item: Extract<Inspect, { kind: 'proc' }>; live: Live } = $props();

  const SHOW = 10;

  let g = $state.raw<ProcGroup | null>(null);
  let seen = $state(false);
  let partial = $state(false);
  let cpu = $state.raw<[number, number][]>([]);
  let mem = $state.raw<[number, number][]>([]);

  $effect(() =>
    live.subscribe('procs', (data) => {
      const s = data as ProcSnapshot;
      if (!s || !Array.isArray(s.groups)) return;
      seen = true;
      partial = s.partial === true;
      g = s.groups.find((x) => x.id === item.group) ?? null;
      if (!g) return;
      const ts = Date.parse(s.t) / 1000 || Date.now() / 1000;
      if (!partial) cpu = push(cpu, ts, g.cpu);
      mem = push(mem, ts, g.mem);
    }),
  );

  const unitScope = $derived(g?.kind === 'service' || g?.kind === 'container' ? 'system' : g?.kind === 'user-service' || g?.kind === 'app' ? 'user' : null);
  const container = $derived(item.group.startsWith('container:') ? item.group.slice('container:'.length) : null);
  const links = $derived.by((): Link[] => {
    if (!g) return [];
    const out: Link[] = [{ label: g.name, item: null }];
    if (g.unit && unitScope && g.kind !== 'container') out.push({ label: g.unit, item: { kind: 'unit', scope: unitScope, name: g.unit } });
    if (container) out.push({ label: container.slice(0, 12), item: { kind: 'container', id: container } });
    return out;
  });
  // The kernel's and the session's own processes are not Hearth's to end.
  const endable = $derived(g !== null && g.kind !== 'kernel' && g.kind !== 'session' && item.group !== 'unit:init.scope' && item.group !== 'user-unit:init.scope');
</script>

<h2 id="inspect-title">{g?.name ?? item.group}</h2>
<p class="sub">{t('inspect.kind.proc')}</p>

{#if seen && !g}
  <p class="muted">{t('inspect.gone')}</p>
{:else if g}
  <dl>
    <dt>{t('inspect.user')}</dt>
    <dd>{g.user}</dd>
    {#if g.unit}<dt>{t('inspect.unit')}</dt><dd class="mono">{g.unit}</dd>{/if}
    <dt>{t('inspect.count')}</dt>
    <dd>{g.count}</dd>
  </dl>

  <h3>{t('inspect.usage')}</h3>
  <MiniChart points={cpu} label={t('inspect.cpu')} value={partial ? '–' : pct(g.cpu, 1)} />
  <MiniChart points={mem} label={t('inspect.mem')} value={bytes(g.mem)} />

  <h3>{t('inspect.top')}</h3>
  <table>
    <tbody>
      {#each [...g.procs].sort((a, b) => b.cpu - a.cpu).slice(0, SHOW) as p (p.pid)}
        <tr><td class="mono muted">{p.pid}</td><td class="name">{p.name}</td><td class="mono n">{pct(p.cpu, 1)}</td><td class="mono n">{bytes(p.rss)}</td></tr>
      {/each}
    </tbody>
  </table>

  <h3>{t('inspect.actions')}</h3>
  <div class="actions">
    {#if endable}
      <Act kind="proc" action="terminate" target={`group:${g.id}`} name={g.name} label={t('inspect.end')} />
    {/if}
    <a href={href('resources')}>{t('inspect.fullPage')}</a>
  </div>

  <Chain {links} />
{:else}
  <p class="muted">{t('inspect.loading')}</p>
{/if}

<style>
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  td {
    padding: 2px 0;
    padding-inline-end: var(--space-2);
  }
  .name {
    max-width: 160px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .n {
    text-align: end;
  }
</style>

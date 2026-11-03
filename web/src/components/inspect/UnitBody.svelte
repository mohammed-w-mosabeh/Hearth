<script lang="ts">
  import Skeleton from '../Skeleton.svelte';
  import { failure } from '../../lib/errors';
  import Act from './Act.svelte';
  import Chain from './Chain.svelte';
  import MiniChart from './MiniChart.svelte';
  import { api } from '../../lib/api';
  import { bytes, pct } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import { push, type Inspect, type Link } from '../../lib/inspect';
  import { lineagePackages, type Lineage } from '../../lib/network';
  import { href } from '../../lib/router.svelte';
  import { available, key, type ServicesMsg, type Unit } from '../../lib/services';
  import type { Live } from '../../lib/ws';

  // A systemd unit. Following "services" while the drawer is open gives
  // its memory and CPU every few seconds.
  let { item, live }: { item: Extract<Inspect, { kind: 'unit' }>; live: Live } = $props();

  const k = $derived(`${item.scope}/${item.name}`);
  let unit = $state.raw<Unit | null>(null);
  let missing = $state(false);
  let failed = $state('');
  let lineage = $state.raw<Lineage | null>(null);
  let cpu = $state.raw<[number, number][]>([]);
  let mem = $state.raw<[number, number][]>([]);
  let now = $state<{ cpu: number; mem: number } | null>(null);

  function take(units: Unit[]): void {
    unit = units.find((u) => key(u) === k) ?? null;
    missing = unit === null;
  }

  function reload(): void {
    api<ServicesMsg>('/services')
      .then((m) => m.kind === 'list' && take(m.units))
      .catch((err: unknown) => (failed = failure(err, 'inspect.error')));
  }

  $effect(() => {
    reload();
    const q = new URLSearchParams({ scope: item.scope, unit: item.name });
    api<Lineage>(`/net/lineage?${q.toString()}`)
      .then((l) => (lineage = l))
      .catch(() => {});
    return live.subscribe('services', (data) => {
      const m = data as ServicesMsg;
      if (m?.kind === 'list' && Array.isArray(m.units)) take(m.units);
      else if (m?.kind === 'stats') {
        const s = m.stats?.[k];
        if (!s) return;
        const ts = Date.now() / 1000;
        now = s;
        cpu = push(cpu, ts, s.cpu);
        mem = push(mem, ts, s.mem);
      }
    });
  });

  const ops = $derived(unit ? available(unit).filter((o) => o === 'start' || o === 'stop' || o === 'restart') : []);
  const opLabel = { start: 'svc.op.start', stop: 'svc.op.stop', restart: 'svc.op.restart' } as const;
  const group = $derived(`${item.scope === 'user' ? 'user-unit' : 'unit'}:${item.name}`);
  const links = $derived.by((): Link[] => {
    const out: Link[] = [];
    if (unit && (unit.active === 'active' || unit.active === 'reloading')) out.push({ label: t('inspect.processes'), item: { kind: 'proc', group } });
    out.push({ label: item.name, item: null });
    for (const p of lineagePackages(lineage)) out.push({ label: p.name, item: { kind: 'package', name: p.name } });
    return out;
  });
</script>

<h2 id="inspect-title">{item.name}</h2>
<p class="sub">{unit?.description ?? t('inspect.kind.unit')}</p>

{#if failed}
  <p class="error">{failed}</p>
{:else if missing}
  <p class="muted">{t('inspect.gone')}</p>
{:else if unit}
  <dl>
    <dt>{t('inspect.state')}</dt>
    <dd>{unit.active} ({unit.sub})</dd>
    {#if unit.enabled}<dt>{t('inspect.enabled')}</dt><dd>{unit.enabled}</dd>{/if}
    <dt>{t('inspect.scope')}</dt>
    <dd>{t(item.scope === 'user' ? 'inspect.scope.user' : 'inspect.scope.system')}</dd>
    {#if lineage?.unit?.main_pid}<dt>{t('inspect.pid')}</dt><dd class="mono">{lineage.unit.main_pid}</dd>{/if}
    {#if lineage?.exe}<dt>{t('inspect.program')}</dt><dd class="mono">{lineage.exe}</dd>{/if}
    {#if lineage?.unit?.file}<dt>{t('inspect.file')}</dt><dd class="mono">{lineage.unit.file}</dd>{/if}
  </dl>

  <h3>{t('inspect.usage')}</h3>
  <MiniChart points={cpu} label={t('inspect.cpu')} value={now ? pct(now.cpu, 1) : '–'} />
  <MiniChart points={mem} label={t('inspect.mem')} value={now ? bytes(now.mem) : '–'} />

  <h3>{t('inspect.actions')}</h3>
  <div class="actions">
    {#each ops as op (op)}
      <Act kind="unit" action={op} target={k} name={item.name} label={t(opLabel[op])} system={item.scope === 'system'} onchanged={reload} />
    {/each}
    <a href={href('services', item.name)}>{t('inspect.fullPage')}</a>
  </div>

  <Chain {links} />
{:else}
  <Skeleton label={t('inspect.loading')} rows={2} />
{/if}

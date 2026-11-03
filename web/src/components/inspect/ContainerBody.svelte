<script lang="ts">
  import Act from './Act.svelte';
  import Chain from './Chain.svelte';
  import MiniChart from './MiniChart.svelte';
  import { api, ApiError } from '../../lib/api';
  import { mergeStats, imageName, type Container, type Point, type Snapshot, type StatsMsg } from '../../lib/docker';
  import { bytes, pct } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import type { Inspect, Link } from '../../lib/inspect';
  import { href } from '../../lib/router.svelte';
  import type { Live } from '../../lib/ws';

  // A container. Docker is asked through hearth-broker once, and
  // "docker.stats" gives its usage while the drawer is open.
  let { item, live }: { item: Extract<Inspect, { kind: 'container' }>; live: Live } = $props();

  let c = $state.raw<Container | null>(null);
  let missing = $state(false);
  let failed = $state('');
  let history = $state.raw<Record<string, Point[]>>({});

  function reload(): void {
    api<Snapshot>('/docker')
      .then((s) => {
        if (s.state !== 'ok') {
          failed = s.error ?? t('inspect.error');
          return;
        }
        c = s.containers.find((x) => x.id.startsWith(item.id) || x.name === item.id) ?? null;
        missing = c === null;
      })
      .catch((err: unknown) => (failed = err instanceof ApiError ? err.message : t('inspect.error')));
  }

  $effect(() => {
    reload();
    return live.subscribe('docker.stats', (data) => {
      const m = data as StatsMsg;
      if (m?.kind === 'history' || m?.kind === 'sample') history = mergeStats(history, m);
    });
  });

  const points = $derived(c ? (history[c.id] ?? []) : []);
  const last = $derived(points.at(-1) ?? null);
  const cpu = $derived(points.map((p): [number, number] => [p.t, p.cpu]));
  const mem = $derived(points.map((p): [number, number] => [p.t, p.mem]));

  const ops = $derived.by((): ('start' | 'stop' | 'restart')[] => {
    if (!c) return [];
    if (c.state === 'running') return ['stop', 'restart'];
    if (c.state === 'exited' || c.state === 'created') return ['start'];
    return [];
  });
  const opLabel = { start: 'docker.op.start', stop: 'docker.op.stop', restart: 'docker.op.restart' } as const;

  const links = $derived.by((): Link[] => {
    if (!c) return [];
    const out: Link[] = [];
    for (const p of c.ports) {
      if (!p.public || (p.type !== 'tcp' && p.type !== 'udp')) continue;
      const label = `${p.type} ${p.public}`;
      if (!out.some((l) => l.label === label)) out.push({ label, item: { kind: 'port', proto: p.type, port: p.public } });
    }
    out.push({ label: c.name, item: null });
    return out;
  });
</script>

<h2 id="inspect-title">{c?.name ?? item.id}</h2>
<p class="sub">{c ? imageName(c) : t('inspect.kind.container')}</p>

{#if failed}
  <p class="error">{failed}</p>
{:else if missing}
  <p class="muted">{t('inspect.gone')}</p>
{:else if c}
  <dl>
    <dt>{t('inspect.state')}</dt>
    <dd>{c.status || c.state}{c.health ? ` · ${c.health}` : ''}</dd>
    {#if c.project}<dt>{t('inspect.project')}</dt><dd>{c.project}{c.service ? ` / ${c.service}` : ''}</dd>{/if}
    <dt>{t('inspect.id')}</dt>
    <dd class="mono">{c.id.slice(0, 12)}</dd>
    {#if c.networks.length > 0}<dt>{t('inspect.networks')}</dt><dd>{c.networks.join(', ')}</dd>{/if}
    {#if c.mounts.length > 0}<dt>{t('inspect.mounts')}</dt><dd>{c.mounts.length}</dd>{/if}
  </dl>

  {#if c.state === 'running'}
    <h3>{t('inspect.usage')}</h3>
    <MiniChart points={cpu} label={t('inspect.cpu')} value={last ? pct(last.cpu, 1) : '–'} />
    <MiniChart points={mem} label={t('inspect.mem')} value={last ? bytes(last.mem) : '–'} />
  {/if}

  <h3>{t('inspect.actions')}</h3>
  <div class="actions">
    {#each ops as op (op)}
      <Act kind="container" action={op} target={c.id} name={c.name} label={t(opLabel[op])} onchanged={() => setTimeout(reload, 1000)} />
    {/each}
    <a href={href('docker', c.name)}>{t('inspect.fullPage')}</a>
  </div>

  <Chain {links} />
{:else}
  <p class="muted">{t('inspect.loading')}</p>
{/if}

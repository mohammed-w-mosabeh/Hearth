<script lang="ts">
  import Skeleton from '../Skeleton.svelte';
  import { failure } from '../../lib/errors';
  import { api } from '../../lib/api';
  import { bytes } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import { formatInspect, type Inspect } from '../../lib/inspect';
  import type { DebDetail, DebPackage } from '../../lib/packages';
  import { href, inspectHref } from '../../lib/router.svelte';

  // An apt package: dpkg's fields and the installed packages that need it.
  // Removing and upgrading stay on the Packages page, with its preview.
  let { item }: { item: Extract<Inspect, { kind: 'package' }> } = $props();

  const USED_BY = 12;

  let d = $state.raw<DebDetail | null>(null);
  let failed = $state('');

  let missing = $state(false);

  // dpkg wants "name:arch"; a lineage or a dependency gives only the name,
  // so the arch comes from the installed list first.
  async function resolve(name: string): Promise<string | null> {
    if (name.includes(':')) return name;
    const list = await api<{ packages: DebPackage[] }>('/pkgs');
    const p = list.packages.find((x) => x.name === name);
    return p ? `${p.name}:${p.arch}` : null;
  }

  $effect(() => {
    resolve(item.name)
      .then(async (id) => {
        if (id === null) missing = true;
        else d = await api<DebDetail>(`/pkgs/apt/${encodeURIComponent(id)}`);
      })
      .catch((err: unknown) => (failed = failure(err, 'inspect.error')));
  });

  const name = $derived(item.name.replace(/:.*$/, ''));
</script>

<h2 id="inspect-title">{name}</h2>
<p class="sub">{d?.summary ?? t('inspect.kind.package')}</p>

{#if failed}
  <p class="error">{failed}</p>
{:else if missing}
  <p class="muted">{t('inspect.gone')}</p>
{:else if d}
  <dl>
    <dt>{t('inspect.version')}</dt>
    <dd class="mono">{d.version}</dd>
    <dt>{t('inspect.arch')}</dt>
    <dd>{d.arch}</dd>
    <dt>{t('inspect.install')}</dt>
    <dd>{d.auto ? t('pkg.detail.auto') : t('pkg.detail.manual')}{d.hold ? ` · ${t('pkg.tag.held')}` : ''}</dd>
    {#if d.installed_kib}<dt>{t('inspect.size')}</dt><dd>{bytes(d.installed_kib * 1024)}</dd>{/if}
    {#if d.section}<dt>{t('pkg.detail.section')}</dt><dd>{d.section}</dd>{/if}
    {#if d.source}<dt>{t('pkg.detail.source')}</dt><dd>{d.source}</dd>{/if}
    <dt>{t('inspect.files')}</dt>
    <dd>{d.files_truncated ? `${d.files.length}+` : d.files.length}</dd>
  </dl>

  {#if d.reverse_deps.length > 0}
    <h3>{t('inspect.usedBy', { n: d.reverse_deps.length })}</h3>
    <ul class="used">
      {#each d.reverse_deps.slice(0, USED_BY) as r (r)}
        <li><a href={inspectHref(formatInspect({ kind: 'package', name: r }))}>{r}</a></li>
      {/each}
    </ul>
  {/if}

  <h3>{t('inspect.actions')}</h3>
  <div class="actions">
    <a href={href('packages', name)}>{t('inspect.fullPage')}</a>
  </div>

{:else}
  <Skeleton label={t('inspect.loading')} rows={2} />
{/if}

<style>
  .used {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1) var(--space-3);
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .used a {
    color: var(--text);
  }
</style>

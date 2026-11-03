<script lang="ts">
  import Skeleton from '../components/Skeleton.svelte';
  import { failure } from '../lib/errors';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import ManDialog from '../components/ManDialog.svelte';
  import { preview, run, type Plan } from '../lib/actions';
  import { api } from '../lib/api';
  import { menuLeft, type MessageKey, t } from '../lib/i18n';
  import { pkgReasonKey, removeKind } from '../lib/packages';
  import { href, router } from '../lib/router.svelte';
  import { openTool } from '../lib/shell';
  import { toast } from '../lib/toast.svelte';
  import { sourceCounts, view, type SortKey, type SourceFilter, type Tool, type ToolList } from '../lib/tools';

  // Every program in PATH (SPEC 3): what owns it, its version and what it
  // does. Nothing here runs a listed program; the man page is rendered by
  // man on the server.
  let list = $state.raw<ToolList | null>(null);
  let failed = $state('');

  async function load(): Promise<void> {
    try {
      list = await api<ToolList>('/tools');
      failed = '';
    } catch (err) {
      failed = failure(err, 'tools.error');
    }
  }
  $effect(() => {
    void load();
  });

  let search = $state('');
  let source = $state<SourceFilter>('all');
  let sortKey = $state<SortKey>('name');
  let desc = $state(false);
  let limit = $state(300);

  // Opened from the command palette (#/tools?q=<name>).
  $effect(() => {
    if (router.q) {
      search = router.q;
      source = 'all';
    }
  });

  const tools = $derived(list?.tools ?? []);
  const shown = $derived(view(tools, source, search, sortKey, desc));
  const n = $derived(sourceCounts(tools));
  $effect(() => {
    void [source, search];
    limit = 300;
  });

  function sortBy(k: SortKey): void {
    if (sortKey === k) desc = !desc;
    else {
      sortKey = k;
      desc = false;
    }
  }

  const sources: { id: SourceFilter; label: MessageKey }[] = [
    { id: 'all', label: 'tools.src.all' },
    { id: 'apt', label: 'tools.src.apt' },
    { id: 'other', label: 'tools.src.other' },
    { id: 'none', label: 'tools.src.none' },
  ];
  const columns: { key: SortKey | null; label: MessageKey }[] = [
    { key: 'name', label: 'tools.col.name' },
    { key: 'package', label: 'tools.col.package' },
    { key: null, label: 'tools.col.version' },
    { key: null, label: 'tools.col.path' },
  ];

  let menu: HTMLDivElement;
  let subject = $state<Tool | null>(null);
  let manOf = $state<string | null>(null);
  let pending = $state<{ tool: Tool; kind: string; plan: Plan } | null>(null);

  function openMenu(e: MouseEvent, x: Tool): void {
    subject = x;
    const b = (e.currentTarget as HTMLElement).getBoundingClientRect();
    menu.showPopover();
    const w = menu.offsetWidth;
    const h = menu.offsetHeight;
    menu.style.top = `${b.bottom + 4 + h > innerHeight ? Math.max(4, b.top - 4 - h) : b.bottom + 4}px`;
    menu.style.left = `${menuLeft(b, w)}px`;
  }

  function man(x: Tool): void {
    menu.hidePopover();
    manOf = x.name;
  }

  const errText = (err: unknown) => (failure(err, 'action.failed'));

  // Removing the owner is the Packages page's removal: previewed (with
  // everything else it takes along), then confirmed.
  async function removeOwner(x: Tool): Promise<void> {
    menu.hidePopover();
    const kind = x.manager ? removeKind(x.manager) : null;
    if (!kind || !x.package_id) return;
    try {
      const plan = await preview(kind, 'remove', x.package_id);
      const bad = plan.blocked[0];
      if (plan.items.length === 0 && bad) {
        toast(t('tools.cannotRemove', { name: x.package ?? x.name, reason: t(pkgReasonKey(bad.reason), { detail: bad.detail ?? '', user: '' }) }), {
          tone: 'error',
        });
        return;
      }
      pending = { tool: x, kind, plan };
    } catch (err) {
      toast(errText(err), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { tool, kind, plan } = pending;
    pending = null;
    toast(t('pkg.authHint'));
    try {
      const res = await run(kind, 'remove', tool.package_id ?? '', {}, plan.token, typed);
      const bad = res.failed[0];
      if (res.done === 0 && bad) {
        const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(pkgReasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
        toast(t('pkg.failed', { op: t('pkg.op.remove'), name: tool.package ?? tool.name, reason }), { tone: 'error' });
      } else {
        toast(t('pkg.done.remove', { name: tool.package ?? tool.name }));
      }
      void load();
    } catch (err) {
      toast(errText(err), { tone: 'error' });
    }
  }
</script>

<header>
  <h1>{t('tools.title')}</h1>
  <p>{list ? t('tools.count', { n: tools.length, dirs: list.dirs.length }) : t('tools.intro')}</p>
</header>
{#if failed}<p class="note error" role="status">{failed}</p>{/if}

<div class="bar">
  <input type="search" placeholder={t('tools.search')} aria-label={t('tools.search')} bind:value={search} />
  <div class="segmented" role="radiogroup" aria-label={t('tools.src')}>
    {#each sources as s (s.id)}
      <button type="button" role="radio" aria-checked={source === s.id} class:on={source === s.id} onclick={() => (source = s.id)}>
        {t(s.label)} <span class="n">{n[s.id]}</span>
      </button>
    {/each}
  </div>
</div>

{#if list === null && !failed}
  <Skeleton label={t('tools.loading')} />
{:else if shown.length === 0}
  <p class="note">{t('tools.none')}</p>
{:else}
  <div class="scroll">
    <table>
      <thead>
        <tr>
          {#each columns as c (c.label)}
            <th aria-sort={c.key && sortKey === c.key ? (desc ? 'descending' : 'ascending') : 'none'}>
              {#if c.key}
                {@const k = c.key}
                <button type="button" onclick={() => sortBy(k)}>
                  {t(c.label)}{#if sortKey === k}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}
                </button>
              {:else}
                <span class="th">{t(c.label)}</span>
              {/if}
            </th>
          {/each}
          <th><span class="visually-hidden">{t('tools.col.actions')}</span></th>
        </tr>
      </thead>
      <tbody>
        {#each shown.slice(0, limit) as x (x.path)}
          <tr>
            <td class="tname">
              <span class="name">{x.name}</span>
              {#if x.alternative}<span class="tag" title={t('tools.tag.altHint')}>{t('tools.tag.alt')}</span>{/if}
              {#if x.shadowed?.length}
                <span class="tag warn" title={t('tools.tag.shadowsHint', { paths: x.shadowed.join(', ') })}>{t('tools.tag.shadows', { n: x.shadowed.length })}</span>
              {/if}
              {#if x.summary}<span class="desc" title={x.summary}>{x.summary}</span>{/if}
            </td>
            <td>
              {#if x.package}
                <a href={href('packages', x.package)}>{x.package}</a>
                {#if x.manager !== 'apt'}<span class="muted">· {t(`pkg.mgr.${x.manager ?? 'apt'}`)}</span>{/if}
              {:else}
                <span class="muted">{t('tools.unowned')}</span>
              {/if}
            </td>
            <td class="ver" title={x.version}>{x.version || '–'}</td>
            <td class="path" title={x.target ? `${x.path} → ${x.target}` : x.path}>{x.path}</td>
            <td class="act">
              <button type="button" class="more-btn" aria-label={t('tools.actionsFor', { name: x.name })} onclick={(e) => openMenu(e, x)}>⋯</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if shown.length > limit}
    <button type="button" class="showmore" onclick={() => (limit += 300)}>{t('pkg.showMore', { n: shown.length - limit })}</button>
  {/if}
  <p class="foot">{t('tools.footnote')}</p>
{/if}

<!-- Not a role="menu": a group of buttons in a popover (no arrow-key contract). -->
<div bind:this={menu} popover="auto" class="menu" aria-label={subject ? t('tools.actionsFor', { name: subject.name }) : undefined}>
  {#if subject}
    {@const x = subject}
    <a href={href('terminal', openTool(x.name))} onclick={() => menu.hidePopover()}>{t('tools.op.terminal')}</a>
    <button type="button" onclick={() => man(x)}>{t('tools.op.man')}</button>
    {#if x.package}
      <a href={href('packages', x.package)} onclick={() => menu.hidePopover()}>{t('tools.op.package')}</a>
    {/if}
    {#if x.manager && removeKind(x.manager) && x.package_id}
      <hr />
      <button type="button" class="danger" onclick={() => removeOwner(x)}>{t('tools.op.remove', { name: x.package ?? '' })}</button>
    {/if}
  {/if}
</div>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('pkg.confirmTitle', { op: t('pkg.op.remove'), name: pending.tool.package ?? pending.tool.name }) : ''}
  verb={t('pkg.op.remove')}
  body={pending ? t('pkg.confirm.remove', { n: pending.plan.items.length }) : ''}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<ManDialog name={manOf} onclose={() => (manOf = null)} />

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-4);
  }
  input[type='search'] {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    min-width: 220px;
  }
  .segmented {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    overflow: hidden;
  }
  .segmented button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 0;
    border-inline-start: 1px solid var(--border);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .segmented button:first-child {
    border-inline-start: 0;
  }
  .segmented button.on {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .n {
    font-size: 11px;
    opacity: 0.75;
  }
  .note,
  .foot,
  .muted {
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .foot {
    font-size: 12px;
    margin-top: var(--space-2);
  }
  .scroll {
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
    white-space: nowrap;
    border-bottom: 1px solid var(--border);
  }
  th {
    position: sticky;
    top: 0;
    background: var(--surface);
  }
  th button,
  .th {
    font: inherit;
    font-weight: 600;
    color: var(--text-muted);
    background: none;
    border: 0;
    padding: 0;
  }
  th button {
    cursor: pointer;
  }
  .tname {
    max-width: 0;
    width: 45%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .name {
    font-weight: 500;
  }
  .desc {
    color: var(--text-muted);
    font-size: 12px;
    margin-inline-start: var(--space-2);
  }
  .ver,
  .path {
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .path {
    color: var(--text-muted);
    font-family: var(--font-mono, monospace);
    font-size: 12px;
  }
  a {
    color: var(--text);
  }
  .tag {
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
  }
  .tag.warn {
    color: var(--warn);
    border-color: var(--warn);
  }
  .act {
    width: 1%;
    text-align: end;
  }
  .more-btn {
    font: inherit;
    font-size: 16px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 1px solid transparent;
    border-radius: var(--radius-s);
    padding: 2px 8px;
    cursor: pointer;
  }
  .more-btn:hover,
  .more-btn:focus-visible {
    border-color: var(--border);
  }
  .showmore {
    font: inherit;
    font-size: 13px;
    color: var(--accent);
    background: none;
    border: 0;
    padding: var(--space-2) 0;
    cursor: pointer;
  }
  .menu {
    position: fixed;
    inset: auto;
    margin: 0;
    min-width: 180px;
    padding: var(--space-1);
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    font-size: 13px;
  }
  .menu:popover-open {
    display: grid;
  }
  .menu button,
  .menu a {
    font: inherit;
    text-align: start;
    color: var(--text);
    background: none;
    border: 0;
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    text-decoration: none;
  }
  .menu button:hover,
  .menu button:focus-visible,
  .menu a:hover,
  .menu a:focus-visible {
    background: var(--surface-2);
  }
  .menu .danger {
    color: var(--crit);
  }
  .menu hr {
    border: 0;
    border-top: 1px solid var(--border);
    margin: var(--space-1) 0;
    width: 100%;
  }
  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>

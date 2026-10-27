<script lang="ts">
  import ConfirmDialog from './ConfirmDialog.svelte';
  import ImageDetail from './ImageDetail.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { ApiError } from '../lib/api';
  import { imageTitle, imageView, shortID, uniqueSize, type Container, type Image, type ImageFilter, type ImageSort } from '../lib/docker';
  import { bytes } from '../lib/format';
  import { t, type MessageKey } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';

  // Images (ROADMAP 2.6): list, detail, and remove / tag / pull through
  // hearth-broker. Remove is previewed; an image in use needs its name typed.
  let { images, containers }: { images: Image[]; containers: Container[] } = $props();

  let search = $state('');
  let filter = $state<ImageFilter>('all');
  let sortKey = $state<ImageSort>('name');
  let desc = $state(false);
  const rows = $derived(imageView(images, filter, search, sortKey, desc));
  const total = $derived(images.reduce((n, im) => n + im.size, 0));

  function sortBy(k: ImageSort): void {
    if (sortKey === k) desc = !desc;
    else {
      sortKey = k;
      desc = k !== 'name';
    }
  }

  const filters: { id: ImageFilter; label: MessageKey }[] = [
    { id: 'all', label: 'image.filter.all' },
    { id: 'used', label: 'image.filter.used' },
    { id: 'unused', label: 'image.filter.unused' },
    { id: 'dangling', label: 'image.filter.dangling' },
  ];

  let menu: HTMLDivElement;
  let subject = $state<Image | null>(null);
  let detailId = $state<string | null>(null);
  let detailTab = $state<'overview' | 'updates' | 'scan'>('overview');
  let pending = $state<{ im: Image; plan: Plan } | null>(null);
  let busy = $state.raw<ReadonlySet<string>>(new Set());
  const detail = $derived(detailId ? (images.find((im) => im.id === detailId) ?? null) : null);

  function openMenu(e: MouseEvent, im: Image): void {
    subject = im;
    const btn = e.currentTarget as HTMLElement;
    menu.showPopover();
    const r = btn.getBoundingClientRect();
    const w = menu.offsetWidth;
    const h = menu.offsetHeight;
    menu.style.top = `${r.bottom + 4 + h > innerHeight ? Math.max(4, r.top - 4 - h) : r.bottom + 4}px`;
    menu.style.left = `${Math.min(Math.max(4, r.right - w), innerWidth - w - 4)}px`;
  }

  function show(im: Image, tab: 'overview' | 'updates' | 'scan' = 'overview'): void {
    menu.hidePopover();
    detailTab = tab;
    detailId = im.id;
  }

  function setBusy(id: string, on: boolean): void {
    busy = on ? new Set([...busy, id]) : new Set([...busy].filter((x) => x !== id));
  }

  function errorText(err: unknown): string {
    return err instanceof ApiError ? err.message : t('action.failed');
  }

  function failText(res: Result): string | null {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done > 0 || !bad) return null;
    if (bad.reason === 'not-found') return t('image.reason.notFound');
    if (bad.reason === 'error' && bad.detail) return bad.detail;
    return t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: '' });
  }

  async function act(kind: 'remove' | 'tag' | 'pull', key: string, target: string, name: string, params: Record<string, string> = {}, token?: string, typed?: string): Promise<void> {
    setBusy(key, true);
    toast(t('docker.authHint'));
    if (kind === 'pull') toast(t('image.pulling', { name }));
    try {
      const res = await run('image', kind, target, params, token, typed);
      const bad = failText(res);
      if (bad) toast(t('image.failed', { op: t(`image.op.${kind}` as MessageKey), name, reason: bad }), { tone: 'error' });
      else {
        if (kind === 'remove' && detailId === target) detailId = null;
        toast(t(`image.done.${kind}` as MessageKey, { name, tag: params.repo ? `${params.repo}:${params.tag}` : '' }));
      }
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      setBusy(key, false);
    }
  }

  async function remove(im: Image): Promise<void> {
    menu.hidePopover();
    try {
      const plan = await preview('image', 'remove', im.id);
      if (plan.items.length === 0) {
        toast(t('image.failed', { op: t('image.op.remove'), name: imageTitle(im), reason: failText({ done: 0, failed: [], blocked: plan.blocked }) ?? '' }), { tone: 'error' });
        return;
      }
      pending = { im, plan };
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    if (!pending) return;
    const { im, plan } = pending;
    pending = null;
    await act('remove', im.id, im.id, imageTitle(im), {}, plan.token, typed);
  }

  function pull(ref: string, key = ref): void {
    menu.hidePopover();
    void act('pull', key, ref, ref);
  }

  // Tag form.
  let tagDialog: HTMLDialogElement;
  let tagOf = $state<Image | null>(null);
  let tagRepo = $state('');
  let tagTag = $state('');
  const repoOK = $derived(/^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$/.test(tagRepo));
  const tagOK = $derived(/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/.test(tagTag));

  function openTag(im: Image): void {
    menu.hidePopover();
    tagOf = im;
    const first = im.tags[0] ?? '';
    const i = first.lastIndexOf(':');
    tagRepo = i > first.lastIndexOf('/') ? first.slice(0, i) : first;
    tagTag = '';
    tagDialog.showModal();
  }

  function submitTag(e: SubmitEvent): void {
    e.preventDefault();
    if (!tagOf || !repoOK || !tagOK) return;
    const im = tagOf;
    tagDialog.close();
    void act('tag', im.id, im.id, imageTitle(im), { repo: tagRepo, tag: tagTag });
  }
</script>

<div class="bar">
  <input type="search" placeholder={t('image.search')} aria-label={t('image.search')} bind:value={search} />
  <div class="segmented" role="radiogroup" aria-label={t('docker.filter')}>
    {#each filters as f (f.id)}
      <button type="button" role="radio" aria-checked={filter === f.id} class:on={filter === f.id} onclick={() => (filter = f.id)}>{t(f.label)}</button>
    {/each}
  </div>
  <span class="muted">{t('image.total', { n: images.length, size: bytes(total) })}</span>
</div>

{#if images.length === 0}
  <p class="note">{t('image.none')}</p>
{:else if rows.length === 0}
  <p class="note">{t('docker.noMatch')}</p>
{:else}
  <div class="scroll">
    <table>
      <thead>
        <tr>
          <th aria-sort={sortKey === 'name' ? (desc ? 'descending' : 'ascending') : 'none'}>
            <button type="button" onclick={() => sortBy('name')}>{t('image.col.name')}{#if sortKey === 'name'}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}</button>
          </th>
          <th><span class="th">{t('image.col.id')}</span></th>
          <th class="num" aria-sort={sortKey === 'size' ? (desc ? 'descending' : 'ascending') : 'none'}>
            <button type="button" onclick={() => sortBy('size')}>{t('image.col.size')}{#if sortKey === 'size'}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}</button>
          </th>
          <th class="num"><span class="th">{t('image.col.unique')}</span></th>
          <th aria-sort={sortKey === 'created' ? (desc ? 'descending' : 'ascending') : 'none'}>
            <button type="button" onclick={() => sortBy('created')}>{t('image.col.created')}{#if sortKey === 'created'}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}</button>
          </th>
          <th><span class="th">{t('image.col.usedBy')}</span></th>
          <th><span class="visually-hidden">{t('docker.col.actions')}</span></th>
        </tr>
      </thead>
      <tbody>
        {#each rows as im (im.id)}
          {@const u = uniqueSize(im)}
          <tr>
            <td class="name-cell">
              <button type="button" class="link" title={imageTitle(im)} onclick={() => show(im)}>{imageTitle(im)}</button>
              {#if im.tags.length > 1}<span class="tag">+{im.tags.length - 1}</span>{/if}
              {#if im.dangling}<span class="tag">{t('image.dangling')}</span>{/if}
            </td>
            <td class="mono muted">{shortID(im.id)}</td>
            <td class="num">{bytes(im.size)}</td>
            <td class="num muted">{u === null ? '–' : bytes(u)}</td>
            <td class="muted">{new Date(im.created * 1000).toLocaleDateString()}</td>
            <td>{im.containers.length > 0 ? t('image.usedCount', { n: im.containers.length }) : ''}</td>
            <td class="act">
              <button type="button" class="more-btn" aria-label={t('docker.actionsFor', { name: imageTitle(im) })} aria-busy={busy.has(im.id)} onclick={(e) => openMenu(e, im)}>
                {busy.has(im.id) ? '…' : '⋯'}
              </button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="foot">{t('image.footnote')}</p>
{/if}

<div bind:this={menu} popover="auto" class="menu" aria-label={subject ? t('docker.actionsFor', { name: imageTitle(subject) }) : undefined}>
  {#if subject}
    {@const im = subject}
    <button type="button" onclick={() => show(im)}>{t('docker.op.details')}</button>
    {#if im.tags.length > 0}
      <button type="button" onclick={() => show(im, 'updates')}>{t('image.op.checkUpdates')}</button>
      <button type="button" onclick={() => pull(im.tags[0] ?? '', im.id)}>{t('image.op.pullTag', { tag: im.tags[0] ?? '' })}</button>
    {/if}
    <button type="button" onclick={() => show(im, 'scan')}>{t('image.op.scan')}</button>
    <hr />
    <button type="button" onclick={() => openTag(im)}>{t('image.op.tag')}</button>
    <button type="button" class="danger" onclick={() => remove(im)}>{t('image.op.remove')}</button>
  {/if}
</div>

<dialog bind:this={tagDialog} aria-labelledby="tag-title" class="form" onclose={() => (tagOf = null)}>
  <form onsubmit={submitTag}>
    <h2 id="tag-title">{tagOf ? t('image.tagTitle', { name: imageTitle(tagOf) }) : ''}</h2>
    <label>{t('image.tagRepo')}<input type="text" bind:value={tagRepo} autocomplete="off" spellcheck="false" aria-invalid={tagRepo !== '' && !repoOK} /></label>
    <label>{t('image.tagTag')}<input type="text" bind:value={tagTag} autocomplete="off" spellcheck="false" aria-invalid={tagTag !== '' && !tagOK} /></label>
    <div class="buttons">
      <button type="button" onclick={() => tagDialog.close()}>{t('confirm.cancel')}</button>
      <button type="submit" class="primary" disabled={!repoOK || !tagOK}>{t('image.op.tag')}</button>
    </div>
  </form>
</dialog>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('image.confirmTitle', { name: imageTitle(pending.im) }) : ''}
  verb={t('image.op.remove')}
  {...pending?.plan.confirm ? { warning: t('image.confirmInUse') } : {}}
  body={t('image.confirmBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<ImageDetail image={detail} {containers} initialTab={detailTab} onpull={(ref) => pull(ref)} onclose={() => (detailId = null)} />

<style>
  .bar {
    display: flex;
    gap: var(--space-3);
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: var(--space-4);
  }
  input[type='search'],
  input[type='text'] {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    min-width: 220px;
  }
  input[aria-invalid='true'] {
    border-color: var(--crit);
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
  .note,
  .foot,
  .muted {
    color: var(--text-muted);
  }
  .muted {
    font-size: 13px;
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
  .num {
    text-align: end;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .name-cell {
    max-width: 380px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .link {
    display: inline-block;
    max-width: 340px;
    overflow: hidden;
    text-overflow: ellipsis;
    vertical-align: bottom;
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
  .tag {
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    margin-inline-start: var(--space-2);
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
  .menu {
    position: fixed;
    inset: auto;
    margin: 0;
    min-width: 200px;
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
  .menu button {
    font: inherit;
    text-align: start;
    color: var(--text);
    background: none;
    border: 0;
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .menu button:hover,
  .menu button:focus-visible {
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
  dialog.form {
    width: min(440px, calc(100vw - 32px));
    padding: var(--space-5);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog.form::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  .form h2 {
    font-size: 17px;
    margin-bottom: var(--space-3);
    overflow-wrap: anywhere;
  }
  .form label {
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
    margin-bottom: var(--space-3);
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
  }
  .buttons button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-4);
    cursor: pointer;
  }
  .buttons .primary {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
  .buttons button:disabled {
    opacity: 0.5;
    cursor: default;
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

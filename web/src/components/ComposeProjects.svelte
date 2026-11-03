<script lang="ts">
  import { failure } from '../lib/errors';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
    import { finished, projectOps, projectState, projectView, tone, type Container, type Project, type ProjectOp } from '../lib/docker';
  import { t, type MessageKey } from '../lib/i18n';
  import { toast } from '../lib/toast.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';

  // Compose projects (ROADMAP 2.7): containers grouped by their project
  // label, with start / stop / restart of the whole project through
  // hearth-broker (dependencies first on start, last on stop).
  let { projects, containers, onopen }: { projects: Project[]; containers: Container[]; onopen: (c: Container) => void } = $props();

  let search = $state('');
  let open = $state.raw<ReadonlySet<string>>(new Set());
  let busy = $state.raw<ReadonlySet<string>>(new Set());
  const byId = $derived(new Map(containers.map((c) => [c.id, c])));
  const rows = $derived(projectView(projects, search, byId));

  function toggle(name: string): void {
    open = open.has(name) ? new Set([...open].filter((x) => x !== name)) : new Set([...open, name]);
  }

  function members(p: Project): Container[] {
    return p.containers
      .map((id) => byId.get(id))
      .filter((c): c is Container => c !== undefined)
      .sort((a, b) => ((a.service ?? a.name) < (b.service ?? b.name) ? -1 : 1));
  }

  const opLabels: Record<ProjectOp, MessageKey> = { start: 'docker.op.start', stop: 'docker.op.stop', restart: 'docker.op.restart' };
  const doneLabels: Record<ProjectOp, MessageKey> = { start: 'compose.done.start', stop: 'compose.done.stop', restart: 'compose.done.restart' };
  const stateLabels = { running: 'compose.state.running', partial: 'compose.state.partial', stopped: 'compose.state.stopped' } as const;

  function report(op: ProjectOp, p: Project, res: Result): void {
    const bad = [...res.blocked, ...res.failed];
    if (bad.length === 0) {
      toast(t(doneLabels[op], { name: p.name }), op === 'stop' ? { undo: () => void act('start', p) } : {});
      return;
    }
    const first = bad[0];
    const reason =
      first?.reason === 'error' && first.detail ? first.detail : first?.reason === 'not-found' ? t('compose.reason.gone') : t(reasonKey(first?.reason ?? 'error'), { detail: first?.detail ?? '', user: '' });
    toast(t('compose.partial', { op: t(opLabels[op]), name: p.name, done: res.done, failed: bad.length, who: first?.name ?? '', reason }), { tone: 'error' });
  }

  // Pull and recreate: the preview lists each service with what happens to its
  // image digest, or what stops the project from being updated.
  let updating = $state.raw<{ project: Project; plan: Plan } | null>(null);

  async function askUpdate(p: Project): Promise<void> {
    busy = new Set([...busy, p.name]);
    try {
      updating = { project: p, plan: await preview('project', 'update', p.name) };
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = new Set([...busy].filter((x) => x !== p.name));
    }
  }

  async function update(): Promise<void> {
    if (!updating) return;
    const { project: p, plan } = updating;
    updating = null;
    busy = new Set([...busy, p.name]);
    toast(t('compose.updateHint'));
    try {
      const res = await run('project', 'update', p.name, {}, plan.token);
      const first = res.failed[0];
      if (first) toast(t('compose.updateFailed', { name: p.name, reason: first.detail || t(reasonKey(first.reason), { detail: '', user: '' }) }), { tone: 'error' });
      else toast(t('compose.updated', { name: p.name }));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = new Set([...busy].filter((x) => x !== p.name));
    }
  }

  async function act(op: ProjectOp, p: Project): Promise<void> {
    busy = new Set([...busy, p.name]);
    toast(t('docker.authHint'));
    try {
      report(op, p, await run('project', op, p.name));
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = new Set([...busy].filter((x) => x !== p.name));
    }
  }
</script>

<div class="bar">
  <input type="search" placeholder={t('compose.search')} aria-label={t('compose.search')} bind:value={search} />
</div>

{#if projects.length === 0}
  <p class="note">{t('compose.none')}</p>
{:else if rows.length === 0}
  <p class="note">{t('docker.noMatch')}</p>
{:else}
  <ul class="projects">
    {#each rows as p (p.name)}
      {@const st = projectState(p, byId)}
      {@const done = finished(p, byId)}
      {@const expanded = open.has(p.name)}
      <li class="card">
        <div class="head">
          <button type="button" class="toggle" aria-expanded={expanded} onclick={() => toggle(p.name)}>
            <span class="chev" aria-hidden="true">{expanded ? '▾' : '▸'}</span>
            <span class="dot {st}" aria-hidden="true"></span>
            <span class="name">{p.name}</span>
          </button>
          <span class="muted"
            >{t(stateLabels[st], { running: p.running, n: p.containers.length - (st === 'running' ? done : 0) })}{#if st === 'running' && done > 0}
              · {t('compose.finished', { n: done })}{/if}</span
          >
          <span class="spacer"></span>
          {#each projectOps(p, byId) as op (op)}
            <button type="button" class="plain" disabled={busy.has(p.name)} onclick={() => act(op, p)}>{t(opLabels[op])}</button>
          {/each}
          <button type="button" class="plain" disabled={busy.has(p.name)} onclick={() => askUpdate(p)}>{t('compose.update')}</button>
        </div>
        <div class="meta">
          {#if p.working_dir}<span class="mono" title={t('compose.dir')}>{p.working_dir}</span>{/if}
          {#each p.config_files as f (f)}<span class="mono file" title={t('compose.file')}>{f}</span>{/each}
        </div>
        <p class="services muted">{t('compose.services', { list: p.services.join(', ') || '–' })}</p>
        {#if expanded}
          <table>
            <tbody>
              {#each members(p) as c (c.id)}
                <tr>
                  <td><span class="dot {tone(c)}" aria-hidden="true"></span><button type="button" class="link" onclick={() => onopen(c)}>{c.name}</button></td>
                  <td class="muted">{c.service ?? ''}</td>
                  <td class="muted">{c.image}</td>
                  <td>{c.status}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </li>
    {/each}
  </ul>
  <p class="foot">{t('compose.footnote')}</p>
{/if}

<ConfirmDialog
  plan={updating?.plan ?? null}
  title={t('compose.updateTitle', { name: updating?.project.name ?? '' })}
  verb={t('compose.update')}
  body={t('compose.updateBody')}
  onconfirm={update}
  oncancel={() => (updating = null)}
/>

<style>
  .bar {
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
    min-width: 260px;
  }
  .projects {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--space-3);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    padding: var(--space-3) var(--space-4);
    font-size: 13px;
  }
  .head {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    flex-wrap: wrap;
  }
  .toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    font: inherit;
    color: var(--text);
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .chev {
    color: var(--text-muted);
    width: 1em;
  }
  .name {
    font-weight: 600;
    font-size: 14px;
  }
  .spacer {
    flex: 1;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1) var(--space-3);
    margin: var(--space-2) 0 0 calc(1em + var(--space-2));
    color: var(--text-muted);
  }
  .services {
    margin: var(--space-1) 0 0 calc(1em + var(--space-2));
  }
  .file::before {
    content: '· ';
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: var(--space-3);
  }
  td {
    padding: 4px var(--space-3) 4px 0;
    border-top: 1px solid var(--border);
    white-space: nowrap;
  }
  .link {
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
  .plain {
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 2px var(--space-3);
    cursor: pointer;
  }
  .plain:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .note,
  .foot,
  .muted {
    color: var(--text-muted);
  }
  .foot {
    font-size: 12px;
    margin-top: var(--space-2);
  }
  .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    margin-inline-end: var(--space-2);
    background: var(--border);
  }
  .dot.running {
    background: var(--ok);
  }
  .dot.partial,
  .dot.changing,
  .dot.paused {
    background: var(--warn);
  }
  .dot.failed {
    background: var(--crit);
  }
</style>

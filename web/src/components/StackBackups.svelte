<script lang="ts">
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { api } from '../lib/api';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { failure } from '../lib/errors';
  import { bytes } from '../lib/format';
  import { locale, t } from '../lib/i18n';
  import { backupQuery, otherFS, type BackupList, type BackupPreview } from '../lib/stacks';
  import { toast } from '../lib/toast.svelte';

  // An app's backups (ROADMAP 9.10): the archives, newest first, and Back
  // up, which shows the sizes and free space before anything runs. Restore
  // overwrites the app's volumes and secrets.env, never a bind folder, and
  // needs the app's name typed back.
  let { name }: { name: string } = $props();

  let list = $state.raw<BackupList | null>(null);
  let error = $state('');
  let planning = $state(false);
  let sizes = $state.raw<BackupPreview | null>(null);
  let stopFirst = $state(true);
  let keep = $state(7);
  let include = $state.raw<string[]>([]);
  let busy = $state(false);
  let confirming = $state.raw<{ action: 'backup' | 'restore'; plan: Plan; params: Record<string, string> } | null>(null);

  async function load(): Promise<void> {
    try {
      list = await api<BackupList>(`/app-backups/${encodeURIComponent(name)}`);
      error = '';
    } catch (err) {
      error = failure(err, 'stacks.backupsError');
    }
  }

  $effect(() => {
    void name;
    void load();
  });

  async function measure(): Promise<void> {
    planning = true;
    sizes = null;
    try {
      sizes = await api<BackupPreview>(`/app-backups/${encodeURIComponent(name)}/preview${backupQuery(keep, include)}`);
    } catch (err) {
      toast(failure(err, 'stacks.backupsError'), { tone: 'error' });
      planning = false;
    }
  }

  function toggle(path: string): void {
    include = include.includes(path) ? include.filter((p) => p !== path) : [...include, path].sort();
    void measure();
  }

  function report(res: Result, done: string): void {
    const first = [...res.blocked, ...res.failed][0];
    if (!first) {
      toast(done);
      return;
    }
    toast(t('stacks.failed', { name, reason: first.detail || t(reasonKey(first.reason), { detail: '', user: '' }) }), { tone: 'error' });
  }

  async function ask(action: 'backup' | 'restore', params: Record<string, string>): Promise<void> {
    busy = true;
    try {
      const plan = await preview('stack', action, name, params);
      confirming = { action, plan, params };
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }

  function backUp(): void {
    void ask('backup', { stop: stopFirst ? 'true' : 'false', keep: String(keep), include: JSON.stringify(include) });
  }

  async function confirm(typed: string): Promise<void> {
    if (!confirming) return;
    const { action, plan, params } = confirming;
    confirming = null;
    busy = true;
    toast(t(action === 'backup' ? 'stacks.backupRunning' : 'docker.authHint'));
    try {
      report(await run('stack', action, name, params, plan.token, typed), t(action === 'backup' ? 'stacks.backedUp' : 'stacks.restored', { name }));
      if (action === 'backup') {
        planning = false;
        sizes = null;
      }
      await load();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }

  function when(iso: string): string {
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(locale('en-GB'), { dateStyle: 'medium', timeStyle: 'short' });
  }
</script>

{#if error}
  <p class="error" role="alert">{error}</p>
{/if}

{#if !planning}
  <button type="button" class="plain" disabled={busy} onclick={measure}>{t('stacks.backupNow')}</button>
{:else}
  <div class="panel">
    {#if sizes === null}
      <p class="muted">{t('stacks.measuring')}</p>
    {:else}
      <p>
        {t(sizes.partial ? 'stacks.backupSizeAtLeast' : 'stacks.backupSize', { size: bytes(sizes.estimate), free: bytes(sizes.free) })}
      </p>
      {#if !sizes.enough}
        <p class="danger" role="note"><span aria-hidden="true">▲</span> {t('stacks.noSpace')}</p>
      {/if}
      <ul class="list">
        {#each sizes.volumes as v (v.name)}
          <li><span class="mono">{v.name}</span> <span class="muted">· {v.included ? bytes(v.size) : t('stacks.leftOut', { why: v.why ?? '' })}</span></li>
        {/each}
        {#each sizes.binds as b (b.real)}
          <li>
            {#if b.other_fs}
              <label class="check"><input type="checkbox" checked={include.includes(b.real)} onchange={() => toggle(b.real)} /> <span class="mono">{b.real}</span></label>
              <span class="muted">· {t('stacks.otherFS')}{#if b.included} · {bytes(b.size)}{/if}</span>
            {:else}
              <span class="mono">{b.real}</span> <span class="muted">· {bytes(b.size)}</span>
            {/if}
          </li>
        {/each}
      </ul>
      {#if otherFS(sizes).length > 0}<p class="muted note">{t('stacks.otherFSHint')}</p>{/if}
      <div class="row">
        <label class="check"><input type="checkbox" bind:checked={stopFirst} /> {t('stacks.stopFirst')}</label>
        <label class="check">
          {t('stacks.keep')}
          <input type="number" min="1" max="30" bind:value={keep} onchange={measure} />
        </label>
      </div>
      <div class="row">
        <button type="button" class="plain primary" disabled={busy || !sizes.enough} onclick={backUp}>{t('stacks.backUp')}</button>
        <button type="button" class="plain" disabled={busy} onclick={() => ((planning = false), (sizes = null))}>{t('stacks.cancel')}</button>
      </div>
    {/if}
  </div>
{/if}

{#if list}
  <ul class="list archives">
    {#each list.archives as a (a.archive)}
      <li>
        <span>{when(a.created)}</span>
        <span class="muted">· {bytes(a.size)} · {t(a.stopped_first ? 'stacks.stoppedFirst' : 'stacks.live')}</span>
        {#if a.warning}<span class="chip warn"><span aria-hidden="true">▲</span> {t('stacks.inconsistent')}</span>{/if}
        <span class="spacer"></span>
        <button type="button" class="plain" disabled={busy} onclick={() => ask('restore', { archive: a.archive })}>{t('stacks.restore')}</button>
      </li>
    {:else}
      <li class="muted">{t('stacks.noBackups')}</li>
    {/each}
  </ul>
  <p class="muted note">{t('stacks.backupsWhere', { dir: list.dir })}</p>
{/if}

<ConfirmDialog
  plan={confirming?.plan ?? null}
  title={t(confirming?.action === 'restore' ? 'stacks.restoreTitle' : 'stacks.backupTitle', { name })}
  verb={t(confirming?.action === 'restore' ? 'stacks.restore' : 'stacks.backUp')}
  body={t(confirming?.action === 'restore' ? 'stacks.restoreBody' : 'stacks.backupBody')}
  onconfirm={confirm}
  oncancel={() => (confirming = null)}
/>

<style>
  .panel {
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-2) var(--space-3);
    font-size: 13px;
    display: grid;
    gap: var(--space-2);
  }
  .panel p {
    margin: 0;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--space-1);
    font-size: 13px;
  }
  .archives {
    margin-top: var(--space-2);
  }
  .archives li,
  .row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  .spacer {
    flex: 1;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
  }
  input[type='number'] {
    font: inherit;
    width: 4em;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 0 var(--space-1);
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .muted {
    color: var(--text-muted);
  }
  .note {
    font-size: 12px;
    margin: var(--space-2) 0 0;
  }
  .chip {
    font-size: 12px;
    border: 1px solid var(--warn);
    border-radius: 999px;
    padding: 0 var(--space-2);
  }
  .danger,
  .error {
    margin: 0;
    padding: var(--space-2) var(--space-3);
    border: 1px solid var(--crit);
    border-radius: var(--radius-s);
    font-size: 13px;
  }
  .plain {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 2px var(--space-3);
    cursor: pointer;
  }
  .plain.primary {
    border-color: var(--accent);
  }
  .plain:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>

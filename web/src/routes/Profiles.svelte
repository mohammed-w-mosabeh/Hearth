<script lang="ts">
  import { failure } from '../lib/errors';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import ProfileDialog from '../components/ProfileDialog.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../lib/actions';
  import { api } from '../lib/api';
  import { locale, t } from '../lib/i18n';
  import { TEMPLATES, doKey, draftFrom, kindKey, templateKey, type Do, type Draft, type Profile } from '../lib/profiles';
  import { toast } from '../lib/toast.svelte';
  import type { Live } from '../lib/ws';

  let { live }: { live: Live } = $props();

  // A game can turn a profile on while this page is open.
  const REFRESH = 15_000;

  let profiles = $state.raw<Profile[] | null>(null);
  let failed = $state('');
  let busy = $state<number | null>(null);
  let draft = $state<Draft | null>(null);
  let pending = $state<{ profile: Profile; plan: Plan } | null>(null);
  let removing = $state<{ profile: Profile; plan: Plan } | null>(null);

  async function load(): Promise<void> {
    try {
      profiles = (await api<{ profiles: Profile[] }>('/profiles')).profiles;
      failed = '';
    } catch (err) {
      failed = failure(err, 'profiles.error');
    }
  }

  $effect(() => {
    void load();
    const timer = setInterval(() => void load(), REFRESH);
    return () => clearInterval(timer);
  });

  function report(name: string, res: Result): void {
    const first = res.failed[0];
    if (first) {
      toast(t('profiles.failed', { s: name, n: res.failed.length, why: t(reasonKey(first.reason), { detail: first.detail ?? '' }) }), { tone: 'error' });
    } else {
      toast(t('profiles.done', { s: name, n: res.done }));
    }
  }

  function toastFailure(err: unknown): void {
    toast(failure(err, 'action.failed'), { tone: 'error' });
  }

  /** Shows what will really change; starting things alone needs no dialog. */
  async function activate(p: Profile): Promise<void> {
    busy = p.id;
    try {
      const plan = await preview('profile', 'activate', String(p.id));
      if (plan.token || plan.blocked.length > 0) {
        const items = plan.items.map((it) => ({ ...it, detail: t(doKey(it.detail as Do)) }));
        pending = { profile: p, plan: { ...plan, items } };
        return;
      }
      report(p.name, await run('profile', 'activate', String(p.id)));
      await load();
    } catch (err) {
      toastFailure(err);
    } finally {
      busy = null;
    }
  }

  async function confirmed(): Promise<void> {
    const c = pending;
    pending = null;
    if (!c) return;
    busy = c.profile.id;
    try {
      report(c.profile.name, await run('profile', 'activate', String(c.profile.id), {}, c.plan.token));
      await load();
    } catch (err) {
      toastFailure(err);
    } finally {
      busy = null;
    }
  }

  async function deactivate(p: Profile): Promise<void> {
    busy = p.id;
    try {
      report(p.name, await run('profile', 'deactivate', String(p.id)));
      await load();
    } catch (err) {
      toastFailure(err);
    } finally {
      busy = null;
    }
  }

  async function removed(): Promise<void> {
    const c = removing;
    removing = null;
    if (!c) return;
    try {
      await api(`/profiles/${c.profile.id}`, { method: 'DELETE' });
      await load();
    } catch (err) {
      toastFailure(err);
    }
  }

  function askRemove(p: Profile): void {
    removing = { profile: p, plan: { summary: '', items: [{ id: String(p.id), name: p.name }], blocked: [], risk: 'destructive' } };
  }

  const when = (iso: string) => new Date(iso).toLocaleString(locale());
</script>

<header>
  <div>
    <h1>{t('profiles.title')}</h1>
    <p>{t('profiles.intro')}</p>
  </div>
  <button type="button" class="btn" onclick={() => (draft = { name: '', items: [], auto_game: false })}>{t('profiles.new')}</button>
</header>

{#if failed}
  <p class="note error" role="status">{failed}</p>
{:else if profiles === null}
  <p class="note">{t('profiles.loading')}</p>
{:else if profiles.length === 0}
  <p class="note">{t('profiles.none')}</p>
  <div class="templates">
    {#each TEMPLATES as tpl (tpl)}
      <button type="button" class="btn" onclick={() => (draft = draftFrom(tpl, t(templateKey(tpl))))}>{t(templateKey(tpl))}</button>
    {/each}
  </div>
{:else}
  <div class="cards">
    {#each profiles as p (p.id)}
      <article class:on={p.active} aria-labelledby={`profile-${p.id}`}>
        <div class="head">
          <h2 id={`profile-${p.id}`}>{p.name}</h2>
          {#if p.active}<span class="badge on">{t('profiles.active')}</span>{/if}
          {#if p.auto_game}<span class="badge">{t('profiles.autoBadge')}</span>{/if}
        </div>
        {#if p.active}
          <p class="note">{t(p.active.by === 'game' ? 'profiles.since.game' : 'profiles.since.user', { time: when(p.active.since) })}</p>
        {/if}
        <ul>
          {#each p.items as it (it.kind + it.target)}
            <li>
              <span class="do {it.do}">{t(doKey(it.do))}</span>
              <span class="target">{it.target}</span>
              <span class="kind">{t(kindKey(it.kind))}</span>
            </li>
          {/each}
        </ul>
        <div class="row">
          {#if p.active}
            <button type="button" class="btn" onclick={() => deactivate(p)} disabled={busy === p.id} aria-busy={busy === p.id}>{t('profiles.deactivate')}</button>
          {:else}
            <button type="button" class="btn" onclick={() => activate(p)} disabled={busy === p.id} aria-busy={busy === p.id}>{t('profiles.activate')}</button>
          {/if}
          <button type="button" class="btn" onclick={() => (draft = { id: p.id, name: p.name, items: p.items, auto_game: p.auto_game })}>{t('profiles.edit')}</button>
          <button type="button" class="btn" onclick={() => askRemove(p)} disabled={p.active !== undefined} title={p.active ? t('profiles.deleteActive') : undefined}>{t('profiles.delete')}</button>
        </div>
      </article>
    {/each}
  </div>
{/if}

<ProfileDialog
  {draft}
  {live}
  onsaved={() => {
    draft = null;
    void load();
  }}
  oncancel={() => (draft = null)}
/>

<ConfirmDialog
  plan={pending?.plan ?? null}
  title={pending ? t('profiles.confirm', { s: pending.profile.name }) : ''}
  verb={t('profiles.activate')}
  body={t('profiles.confirmBody')}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

<ConfirmDialog
  plan={removing?.plan ?? null}
  title={removing ? t('profiles.confirmDelete', { s: removing.profile.name }) : ''}
  verb={t('profiles.delete')}
  body={t('profiles.confirmDeleteBody')}
  onconfirm={removed}
  oncancel={() => (removing = null)}
/>

<style>
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--space-4);
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  h2 {
    font-size: 15px;
    margin: 0;
    overflow-wrap: anywhere;
  }
  header p,
  .note {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .error {
    color: var(--crit);
  }
  .btn {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    white-space: nowrap;
  }
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .templates,
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-3);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 340px), 1fr));
    gap: var(--space-4);
    align-items: start;
  }
  article {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    min-width: 0;
  }
  article.on {
    border-inline-start: 4px solid var(--ok);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: center;
  }
  .badge {
    font-size: 12px;
    padding: 0 var(--space-2);
    border-radius: 999px;
    border: 1px solid currentColor;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .badge.on {
    color: var(--ok);
  }
  ul {
    list-style: none;
    padding: 0;
    margin: var(--space-2) 0 0;
  }
  li {
    display: flex;
    gap: var(--space-2);
    align-items: baseline;
    padding: 2px 0;
    font-size: 13px;
    min-width: 0;
  }
  .do {
    flex: none;
    width: 3.5em;
    color: var(--ok);
    font-size: 12px;
  }
  .do.stop {
    color: var(--warn);
  }
  .target {
    flex: 1 1 auto;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .kind {
    flex: none;
    color: var(--text-muted);
    font-size: 12px;
  }
</style>

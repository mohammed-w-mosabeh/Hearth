<script lang="ts">
  import Skeleton from './Skeleton.svelte';
  import { failure } from '../lib/errors';
  import type { Plan } from '../lib/actions';
  import { api } from '../lib/api';
  import { locale, t } from '../lib/i18n';
  import { acceptBody, diffUrl, type IntegrityChange, type IntegrityDiff, type IntegrityView } from '../lib/integrity';
  import { toast } from '../lib/toast.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';

  // Integrity watch on the Security page (ROADMAP 7.3). Opening the page scans;
  // only "Accept" changes anything, and it names each change as shown here.
  let view = $state.raw<IntegrityView | null>(null);
  let failed = $state('');
  let open = $state<string | null>(null);
  let diff = $state.raw<IntegrityDiff | null>(null);
  let diffFailed = $state('');
  let pending = $state.raw<IntegrityChange[] | null>(null);

  async function load(): Promise<void> {
    try {
      view = await api<IntegrityView>('/security/integrity');
      failed = '';
    } catch (err) {
      failed = failure(err, 'integrity.error');
    }
  }

  async function toggle(c: IntegrityChange): Promise<void> {
    if (open === c.path) {
      open = null;
      return;
    }
    open = c.path;
    diff = null;
    diffFailed = '';
    try {
      const d = await api<IntegrityDiff>(diffUrl(c.path));
      if (open === c.path) diff = d;
    } catch (err) {
      if (open === c.path) diffFailed = failure(err, 'integrity.diffError');
    }
  }

  async function accept(): Promise<void> {
    const changes = pending;
    if (!changes) return;
    try {
      view = await api<IntegrityView>('/security/integrity/accept', { method: 'POST', body: acceptBody(changes) });
      open = null;
      if (view.changes.some((c) => changes.some((p) => p.path === c.path))) {
        toast(t('integrity.moved'), { tone: 'error' });
      }
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
      await load();
    } finally {
      pending = null;
    }
  }

  $effect(() => {
    void load();
  });

  const when = (ms: number) => new Date(ms).toLocaleString(locale());
  const plan = $derived<Plan | null>(
    pending ? { summary: '', items: pending.map((c) => ({ id: c.path, name: c.path })), blocked: [], risk: 'safe' } : null,
  );
</script>

<section class="card" aria-labelledby="integrity-title">
  <h2 id="integrity-title">{t('integrity.title')}</h2>
  {#if failed}
    <p class="muted" role="status">{failed}</p>
  {:else if view === null}
    <Skeleton label={t('security.loading')} rows={2} />
  {:else}
    <p class="muted">{t('integrity.intro')}</p>
    {#if view.error}<p class="muted" role="status">{t('integrity.failed')}</p>{/if}
    {#if view.since > 0}
      <p class="muted">{t('integrity.since', { time: when(view.since), n: view.watched })}</p>
    {/if}

    {#if view.changes.length === 0}
      <p class="ok" role="status">✓ {t('integrity.clean')}</p>
    {:else}
      <div class="row">
        <h3>{t('integrity.changes', { n: view.changes.length + (view.more ?? 0) })}</h3>
        <button type="button" class="btn" onclick={() => (pending = view?.changes ?? null)}>{t('integrity.acceptAll')}</button>
      </div>
      <ul class="changes">
        {#each view.changes as c (c.path)}
          <li>
            <div class="row">
              <span class="kind">{t(`integrity.kind.${c.kind}`)}</span>
              <code>{c.path}</code>
              <span class="grow"></span>
              <button type="button" class="link" aria-expanded={open === c.path} onclick={() => toggle(c)}>{t('integrity.show')}</button>
              <button type="button" class="link" onclick={() => (pending = [c])}>{t('integrity.accept')}</button>
            </div>
            {#if open === c.path}
              {#if diffFailed}
                <p class="muted" role="status">{diffFailed}</p>
              {:else if diff === null}
                <Skeleton label={t('security.loading')} rows={2} />
              {:else if diff.note}
                <p class="muted">{t(`integrity.note.${diff.note}`)}</p>
              {:else}
                <pre class="diff" dir="ltr">{#each diff.lines as l, i (i)}{#if l.gap}<span class="gap">…
</span>{:else}<span class={l.op === '+' ? 'add' : l.op === '-' ? 'del' : ''}>{l.op} {l.text}
</span>{/if}{/each}</pre>
                {#if diff.truncated}<p class="muted">{t('integrity.diffCut')}</p>{/if}
              {/if}
            {/if}
          </li>
        {/each}
      </ul>
      {#if view.more}<p class="muted">{t('integrity.more', { n: view.more })}</p>{/if}
    {/if}
    {#if view.truncated}<p class="muted">{t('integrity.truncated')}</p>{/if}

    <details>
      <summary>{t('integrity.system')}</summary>
      <p class="muted">{t('integrity.systemIntro')}</p>
      <ul class="system">
        {#each view.system as s (s.dir)}
          <li>
            <code>{s.dir}</code>
            {#if !s.readable}
              <span class="muted">{t('integrity.unreadable')}</span>
            {:else if s.names.length === 0}
              <span class="muted">{t('integrity.empty')}</span>
            {:else}
              <span>{s.names.join(', ')}{s.truncated ? ' …' : ''}</span>
            {/if}
          </li>
        {/each}
      </ul>
    </details>
  {/if}
</section>

<ConfirmDialog
  {plan}
  title={t('integrity.confirmTitle')}
  verb={t('integrity.accept')}
  body={t('integrity.confirmBody')}
  onconfirm={accept}
  oncancel={() => (pending = null)}
/>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-top: var(--space-4);
  }
  h2 {
    margin: 0 0 var(--space-2);
    font-size: 1rem;
  }
  h3 {
    margin: 0;
    font-size: 0.875rem;
  }
  .row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  .grow {
    flex: 1;
  }
  .muted {
    color: var(--text-muted);
  }
  .ok {
    margin: var(--space-2) 0;
  }
  .kind {
    font-weight: 600;
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
  }
  .link {
    font: inherit;
    cursor: pointer;
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--accent);
    text-decoration: underline;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: var(--space-2) 0;
    overflow-wrap: anywhere;
  }
  .changes li,
  .system li {
    padding: var(--space-1) 0;
    border-bottom: 1px solid var(--border);
  }
  .changes li:last-child,
  .system li:last-child {
    border-bottom: 0;
  }
  .diff {
    margin: var(--space-1) 0 0;
    padding: var(--space-2);
    max-height: 24rem;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    font-family: var(--font-mono);
    font-size: 0.8125rem;
  }
  .add {
    font-weight: 600;
    color: var(--ok);
  }
  .del {
    text-decoration: line-through;
    color: var(--crit);
  }
  .gap {
    color: var(--text-muted);
  }
</style>

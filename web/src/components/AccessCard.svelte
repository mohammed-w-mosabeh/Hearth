<script lang="ts">
  import { sessionPlace, type AccessReport } from '../lib/access';
  import { api } from '../lib/api';
  import { locale, t } from '../lib/i18n';

  // Access on the Security page (ROADMAP 7.4). Read-only: nothing here acts.
  let report = $state.raw<AccessReport | null>(null);
  let failed = $state(false);

  async function load(): Promise<void> {
    try {
      report = await api<AccessReport>('/security/access');
      failed = false;
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void load();
  });

  const when = (ms: number) => new Date(ms).toLocaleString(locale());
</script>

<section class="card" aria-labelledby="access-title">
  <h2 id="access-title">{t('access.title')}</h2>
  {#if failed}
    <p class="muted" role="status">{t('access.error')}</p>
  {:else if report === null}
    <p class="muted">{t('security.loading')}</p>
  {:else}
    <p class="muted">{t('access.intro')}</p>

    <h3>{t('access.ssh', { n: report.days })}</h3>
    {#if report.limited}
      <p class="muted" role="status">{t('access.limited')}</p>
    {:else if report.sources.length === 0}
      <p class="muted">{t('access.noLogins')}</p>
    {:else}
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th scope="col">{t('access.source')}</th>
              <th scope="col" class="num">{t('access.accepted')}</th>
              <th scope="col" class="num">{t('access.failed')}</th>
              <th scope="col">{t('access.last')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.sources as s (s.source)}
              <tr>
                <td><code dir="ltr">{s.source}</code></td>
                <td class="num">{s.accepted}</td>
                <td class="num">{s.failed}</td>
                <td>{when(s.last)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if report.more}<p class="muted">{t('access.more', { n: report.more })}</p>{/if}
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th scope="col">{t('access.day')}</th>
              <th scope="col" class="num">{t('access.accepted')}</th>
              <th scope="col" class="num">{t('access.failed')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.byDay as d (d.day)}
              <tr>
                <td>{d.day}</td>
                <td class="num">{d.accepted}</td>
                <td class="num">{d.failed}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if report.truncated}<p class="muted">{t('access.cut')}</p>{/if}
    {/if}

    <h3>{t('access.sessions')}</h3>
    {#if report.sessionsError}
      <p class="muted" role="status">{t('access.sessionsError')}</p>
    {:else if report.sessions.length === 0}
      <p class="muted">{t('access.noSessions')}</p>
    {:else}
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th scope="col">{t('access.user')}</th>
              <th scope="col">{t('access.where')}</th>
              <th scope="col">{t('access.since')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.sessions as s (s.id)}
              <tr>
                <td>{s.user}</td>
                <td>{#if s.remote}<code dir="ltr">{sessionPlace(s)}</code>{:else}{t('access.local')}{#if sessionPlace(s)} (<code dir="ltr">{sessionPlace(s)}</code>){/if}{/if}</td>
                <td>{s.since ? when(s.since) : ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    <h3>{t('access.users')}</h3>
    {#if report.usersError}
      <p class="muted" role="status">{t('access.usersError')}</p>
    {:else}
      <p class="muted">{t('access.usersNote')}</p>
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th scope="col">{t('access.user')}</th>
              <th scope="col" class="num">{t('access.uid')}</th>
              <th scope="col">{t('access.flags')}</th>
              <th scope="col">{t('access.groups')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.users as u (u.name)}
              <tr>
                <td>{u.name}</td>
                <td class="num">{u.uid >= 0 ? u.uid : ''}</td>
                <td>
                  {#if u.sudo}<span class="flag">▲ {t('access.flagSudo')}</span>{/if}
                  {#if u.docker}<span class="flag">▲ {t('access.flagDocker')}</span>{/if}
                  {#if u.uid < 0}<span class="muted">{t('access.unknownUser')}</span>{:else if !u.login}<span class="muted">{t('access.noLogin')}</span>{/if}
                </td>
                <td>{u.groups.join(', ')}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</section>

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
    margin: var(--space-3) 0 var(--space-1);
    font-size: 0.875rem;
  }
  .muted {
    color: var(--text-muted);
  }
  .scroll {
    overflow-x: auto;
    margin-block-end: var(--space-2);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    overflow-wrap: anywhere;
  }
  th,
  td {
    text-align: start;
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  th {
    font-weight: 600;
    white-space: nowrap;
  }
  .num {
    text-align: end;
    font-variant-numeric: tabular-nums;
  }
  .flag {
    display: block;
    font-weight: 600;
    color: var(--warn);
  }
</style>

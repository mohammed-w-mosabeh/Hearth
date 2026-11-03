<script lang="ts">
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import { t } from '../lib/i18n';
  import { PERIODS, causeKey, periodKey, rounded, type Cause, type Diagnosis, type Energy, type EnergyPeriod } from '../lib/insight';

  let diagnosis = $state.raw<Diagnosis | null>(null);
  let running = $state(false);
  let fanError = $state('');
  let energy = $state.raw<Energy | null>(null);
  let energyFailed = $state('');
  let period = $state<EnergyPeriod>('week');

  async function explain(): Promise<void> {
    running = true;
    try {
      diagnosis = await api<Diagnosis>('/insight/fan');
      fanError = '';
    } catch (err) {
      fanError = failure(err, 'insight.fan.failed');
    } finally {
      running = false;
    }
  }

  // The period can change again before the previous estimate arrives.
  let seq = 0;

  async function loadEnergy(p: EnergyPeriod): Promise<void> {
    const mine = ++seq;
    try {
      const e = await api<Energy>(`/insight/energy?period=${p}`);
      if (mine !== seq) return;
      energy = e;
      energyFailed = '';
    } catch (err) {
      if (mine === seq) energyFailed = failure(err, 'insight.energy.error');
    }
  }

  $effect(() => {
    void loadEnergy(period);
  });

  function text(c: Cause): string {
    return t(causeKey(c.code), { s: c.subject ?? '', n: Math.round(c.value ?? 0) });
  }
</script>

<header>
  <h1>{t('insight.title')}</h1>
  <p>{t('insight.intro')}</p>
</header>

<section aria-labelledby="fan-title">
  <div class="head">
    <h2 id="fan-title">{t('insight.fan.title')}</h2>
    <button type="button" class="btn" onclick={explain} disabled={running} aria-busy={running}>
      {running ? t('insight.fan.running') : diagnosis ? t('insight.fan.again') : t('insight.fan.run')}
    </button>
  </div>
  {#if fanError}<p class="note error" role="status">{fanError}</p>{/if}
  {#if diagnosis}
    {@const d = diagnosis}
    <ul class="causes">
      {#each d.causes as c, i (i)}<li>{text(c)}</li>{/each}
    </ul>
    <dl>
      <div><dt>{t('insight.cpu')}</dt><dd>{Math.round(d.cpu)}%</dd></div>
      <div><dt>{t('insight.temp')}</dt><dd>{d.temp_c === undefined ? t('insight.unknown') : `${Math.round(d.temp_c)} °C`}</dd></div>
      {#each d.fans as f (f.chip + f.label)}
        <div><dt>{t('insight.fans')}: {f.label}</dt><dd>{Math.round(f.rpm)} RPM</dd></div>
      {/each}
      {#each d.gpus as g (g.name)}
        <div>
          <dt>{t('insight.gpu')}: {g.name}</dt>
          <dd>{g.util === undefined ? t('insight.gpu.asleep') : `${Math.round(g.util)}%`}{g.temp_c === undefined ? '' : ` · ${Math.round(g.temp_c)} °C`}{g.power_w === undefined ? '' : ` · ${rounded(g.power_w)} W`}</dd>
        </div>
      {/each}
    </dl>
    {#if d.fans.length === 0}<p class="note">{t('insight.noFans')}</p>{/if}
    {#if d.top.length > 0}
      <h3>{t('insight.top')}</h3>
      <table>
        <tbody>
          {#each d.top as it (it.id)}
            <tr><td class="wrap">{it.name}</td><td class="n">{rounded(it.cpu)}%</td></tr>
          {/each}
        </tbody>
      </table>
      <p class="note">{t('insight.topHint')}</p>
    {/if}
  {/if}
</section>

<section aria-labelledby="energy-title">
  <div class="head">
    <h2 id="energy-title">{t('insight.energy.title')}</h2>
    <div class="segmented" role="radiogroup" aria-label={t('insight.energy.period')}>
      {#each PERIODS as p (p)}
        <button type="button" role="radio" aria-checked={period === p} class:on={period === p} onclick={() => (period = p)}>{t(periodKey(p))}</button>
      {/each}
    </div>
  </div>
  {#if energyFailed}
    <p class="note error" role="status">{energyFailed}</p>
  {:else if energy}
    {#if energy.items.length === 0}
      <p class="note">{t('insight.energy.none')}</p>
    {:else}
      <table>
        <thead>
          <tr><th>{t('insight.energy.name')}</th><th class="n">{t('insight.energy.wh')}</th><th class="n">{t('insight.energy.share')}</th></tr>
        </thead>
        <tbody>
          {#each energy.items as it (it.item)}
            <tr><td class="wrap">{it.name}</td><td class="n">{t('insight.wh', { n: rounded(it.wh) })}</td><td class="n">{Math.round(it.share)}%</td></tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <p class="note">
      {energy.fitted ? t('insight.energy.fitted', { w: Math.round(energy.full_watts), h: energy.fit_hours }) : t('insight.energy.nominal', { w: Math.round(energy.full_watts) })}
      {t('insight.energy.caveat')}
    </p>
  {/if}
</section>

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  h2 {
    font-size: 16px;
    margin: 0;
  }
  h3 {
    font-size: 14px;
    margin: var(--space-3) 0 var(--space-1);
  }
  header p,
  .note {
    color: var(--text-muted);
    margin: var(--space-1) 0 0;
  }
  .note {
    font-size: 13px;
  }
  .error {
    color: var(--crit);
  }
  section {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-3) var(--space-4);
    margin-bottom: var(--space-4);
    min-width: 0;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-2);
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
  .causes {
    margin: var(--space-3) 0;
    padding-inline-start: var(--space-4);
    font-size: 15px;
  }
  dl {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 200px), 1fr));
    gap: var(--space-2) var(--space-3);
    margin: 0;
  }
  dt {
    font-size: 12px;
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }
  dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    margin-top: var(--space-2);
  }
  th,
  td {
    text-align: start;
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border);
  }
  th {
    color: var(--text-muted);
    font-weight: 600;
  }
  th.n,
  td.n {
    text-align: end;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  .wrap {
    overflow-wrap: anywhere;
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
    color: var(--text-muted);
    background: var(--surface);
    border: 0;
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  .segmented button.on {
    color: var(--text);
    background: var(--surface-2);
  }
</style>

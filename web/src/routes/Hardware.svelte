<script lang="ts">
  import { failure } from '../lib/errors';
  import { api } from '../lib/api';
  import { bytes } from '../lib/format';
  import { deviceName, updatable, type FirmwareReport, type Inventory } from '../lib/hardware';
  import { t } from '../lib/i18n';
  import { href } from '../lib/router.svelte';

  // Hardware (ROADMAP 6.7). Device names come from firmware and id files:
  // plain text, escaped by Svelte. Firmware is asked for by a click, so
  // fwupd is not started on its own (SECURITY S21g).
  let inv = $state.raw<Inventory | null>(null);
  let failed = $state('');
  let firmware = $state.raw<FirmwareReport | null>(null);
  let checking = $state(false);

  $effect(() => {
    api<Inventory>('/hardware')
      .then((r) => (inv = r))
      .catch((err: unknown) => (failed = failure(err, 'hw.error')));
  });

  async function check(): Promise<void> {
    checking = true;
    try {
      firmware = await api<FirmwareReport>('/hardware/firmware');
    } catch {
      firmware = { available: false, devices: [] };
    } finally {
      checking = false;
    }
  }
</script>

<header>
  <h1>{t('hw.title')}</h1>
  <p>{t('hw.intro')}</p>
</header>

{#if failed}<p class="error" role="status">{failed}</p>{/if}
{#if inv}
  {@const i = inv}
  <dl>
    {#if i.vendor || i.product}<div><dt>{t('hw.machine')}</dt><dd>{[i.vendor, i.product].filter(Boolean).join(' ')}</dd></div>{/if}
    {#if i.cpu}<div><dt>{t('hw.cpu')}</dt><dd>{i.cpu} · {t('hw.threads', { n: i.threads })}</dd></div>{/if}
    <div><dt>{t('hw.memory')}</dt><dd>{bytes(i.mem_bytes)}</dd></div>
    {#if i.board}<div><dt>{t('hw.board')}</dt><dd>{i.board}</dd></div>{/if}
    {#if i.bios}<div><dt>{t('hw.bios')}</dt><dd>{i.bios}{i.bios_date ? ` · ${i.bios_date}` : ''}</dd></div>{/if}
  </dl>

  <h2>{t('hw.disks')}</h2>
  {#if i.disks.length === 0}<p class="note">{t('hw.none')}</p>{/if}
  <ul>
    {#each i.disks as d (d.name)}<li><strong>{d.name}</strong> {d.model ?? ''} <span class="note">{bytes(d.bytes)}</span></li>{/each}
  </ul>

  <h2>{t('hw.usb')}</h2>
  {#if i.usb.length === 0}<p class="note">{t('hw.none')}</p>{/if}
  <ul>
    {#each i.usb as d, n (`${d.id}-${n}`)}<li>{deviceName(d)} <span class="note">{d.vendor && d.name ? `${d.vendor} · ` : ''}{d.id}</span></li>{/each}
  </ul>

  <h2>{t('hw.pci')}</h2>
  {#if i.pci.length === 0}<p class="note">{t('hw.none')}</p>{/if}
  <ul>
    {#each i.pci as d, n (`${d.id}-${n}`)}<li>{deviceName(d)} <span class="note">{d.vendor && d.name ? `${d.vendor} · ` : ''}{d.class ? `${d.class} · ` : ''}{d.id}</span></li>{/each}
  </ul>

  <h2>{t('hw.firmware')}</h2>
  <button type="button" onclick={check} disabled={checking}>{t(firmware ? 'hw.fw.again' : 'hw.fw.check')}</button>
  {#if firmware}
    {@const f = firmware}
    {#if !f.available}
      <p class="note">{t('hw.fw.none')}</p>
    {:else if f.devices.length === 0}
      <p class="note">{t('hw.fw.noDevices')}</p>
    {:else}
      <ul>
        {#each f.devices as d (d.id)}
          <li>{d.name} <span class="note">{d.version ?? ''}</span>{#if d.update && d.update !== d.version} <strong>{t('hw.fw.update', { version: d.update })}</strong>{/if}</li>
        {/each}
      </ul>
      {#if updatable(f.devices) > 0}
        <p class="note">{t('hw.fw.apply')} <a href={href('terminal')}>{t('nav.terminal')}</a></p>
      {:else}
        <p class="note">{t('hw.fw.current')}</p>
      {/if}
    {/if}
  {/if}
{/if}

<style>
  header {
    margin-bottom: var(--space-4);
  }
  h1 {
    font-size: 22px;
  }
  header p,
  .note {
    color: var(--text-muted);
  }
  header p {
    margin: var(--space-1) 0 0;
  }
  .note {
    font-size: 12px;
  }
  .error {
    color: var(--crit);
  }
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: var(--space-5) 0 var(--space-2);
  }
  dl {
    display: grid;
    gap: var(--space-1);
    margin: 0;
    font-size: 14px;
  }
  dl div {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  dt {
    color: var(--text-muted);
    min-inline-size: 110px;
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  ul {
    list-style: none;
    margin: var(--space-2) 0 0;
    padding: 0;
    font-size: 13px;
  }
  li {
    padding: 2px 0;
    overflow-wrap: anywhere;
  }
  button {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
  }
  a {
    color: var(--accent);
  }
</style>

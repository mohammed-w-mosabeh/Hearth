<script lang="ts">
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Blocked, type Plan, type Result } from '../lib/actions';
  import { api, ApiError } from '../lib/api';
  import type { App } from '../lib/apps';
  import { applyDensity, loadDensity, saveDensity, type Density } from '../lib/density';
  import { t, type MessageKey } from '../lib/i18n';
  import {
    bars,
    otherConnections,
    savedFor,
    validHostname,
    volumeParam,
    zoneGroups,
    type AccessPoint,
    type AudioDevice,
    type AudioState,
    type AutostartEntry,
    type Connection,
    type NetworkState,
    type SystemInfo,
  } from '../lib/settings';
  import { toast } from '../lib/toast.svelte';
  import type { Power } from '../lib/types';

  let info = $state<SystemInfo | null>(null);
  let net = $state<NetworkState | null>(null);
  let audio = $state<AudioState | null>(null);
  let autostart = $state<AutostartEntry[] | null>(null);
  let power = $state<Power | null>(null);
  let zones = $state<string[]>([]);
  let apps = $state<App[]>([]);
  let failed = $state(false);
  let busy = $state(false);

  let staticName = $state('');
  let prettyName = $state('');
  let addApp = $state('');
  let confirming = $state<{ name: string; title: string; verb: string; plan: Plan } | null>(null);

  const profiles: Record<string, MessageKey> = {
    'power-saver': 'profile.powerSaver',
    balanced: 'profile.balanced',
    performance: 'profile.performance',
  };

  async function loadInfo(): Promise<void> {
    try {
      info = await api<SystemInfo>('/system/info');
      staticName = info.hostname?.static ?? '';
      prettyName = info.hostname?.pretty ?? '';
      failed = false;
    } catch {
      failed = true;
    }
  }
  const loadNet = async () => void (net = await api<NetworkState>('/system/network').catch(() => null));
  const loadAudio = async () => void (audio = await api<AudioState>('/system/audio').catch(() => null));
  let hearthVersion = $state('');
  const loadPower = async () => void (power = await api<Power>('/power').catch(() => null));
  async function loadAutostart(): Promise<void> {
    autostart = await api<{ entries: AutostartEntry[] }>('/system/autostart')
      .then((r) => r.entries)
      .catch(() => null);
  }

  $effect(() => {
    void api<{ version: string }>('/session')
      .then((s) => (hearthVersion = s.version))
      .catch(() => {});
    void loadInfo();
    void loadNet();
    void loadAudio();
    void loadPower();
    void loadAutostart();
    api<{ timezones: string[] }>('/system/timezones')
      .then((r) => (zones = r.timezones))
      .catch(() => {});
    api<{ apps: App[] }>('/apps')
      .then((r) => (apps = r.apps))
      .catch(() => {});
  });

  function errorText(err: unknown): string {
    return err instanceof ApiError ? err.message : t('action.failed');
  }

  function failText(fail: Blocked): string {
    return fail.reason === 'error' && fail.detail ? fail.detail : t(reasonKey(fail.reason), { detail: fail.detail ?? '' });
  }

  /** Runs a non-destructive action, says how it went and reloads. */
  async function act(kind: string, name: string, target: string, params: Record<string, string>, ok: string, reload: () => Promise<void>): Promise<boolean> {
    busy = true;
    try {
      const res: Result = await run(kind, name, target, params);
      const fail = res.failed[0] ?? res.blocked?.[0];
      if (fail) {
        toast(t('set.failed', { why: failText(fail) }), { tone: 'error' });
        return false;
      }
      if (ok) toast(ok);
      return true;
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
      return false;
    } finally {
      busy = false;
      await reload();
    }
  }

  // Network ------------------------------------------------------------

  function apConnect(ap: AccessPoint): void {
    const saved = net ? savedFor(net.connections, ap.ssid) : undefined;
    if (saved) void act('network', 'connect', saved.uuid, {}, t('set.net.connecting', { name: ap.ssid }), loadNet);
    else void act('wifi', 'join', ap.ssid, { device: ap.device }, t('set.net.joining', { name: ap.ssid }), loadNet);
  }

  function apDisconnect(ap: AccessPoint): void {
    const c = net?.connections.find((x) => x.active && x.ssid === ap.ssid);
    if (c) void act('network', 'disconnect', c.uuid, {}, '', loadNet);
  }

  function connToggle(c: Connection): void {
    void act('network', c.active ? 'disconnect' : 'connect', c.uuid, {}, '', loadNet);
  }

  const wifiDevices = $derived(net?.devices.filter((d) => d.type === 'wifi') ?? []);

  // Audio --------------------------------------------------------------

  function setVolume(d: AudioDevice, percent: number): void {
    void act('audio', 'volume', String(d.id), { volume: volumeParam(percent) }, '', loadAudio);
  }

  // Power --------------------------------------------------------------

  async function setProfile(name: string): Promise<void> {
    try {
      await api('/power/profile', { method: 'POST', body: JSON.stringify({ profile: name }) });
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
    await loadPower();
  }

  const powerActions: { name: 'suspend' | 'hibernate' | 'reboot' | 'poweroff'; label: MessageKey; title: MessageKey }[] = [
    { name: 'suspend', label: 'set.power.suspend', title: 'set.power.suspendTitle' },
    { name: 'hibernate', label: 'set.power.hibernate', title: 'set.power.hibernateTitle' },
    { name: 'reboot', label: 'set.power.reboot', title: 'set.power.rebootTitle' },
    { name: 'poweroff', label: 'set.power.poweroff', title: 'set.power.poweroffTitle' },
  ];

  async function askPower(a: (typeof powerActions)[number]): Promise<void> {
    try {
      const plan = await preview('power', a.name, 'machine');
      confirming = { name: a.name, title: t(a.title), verb: t(a.label), plan };
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    }
  }

  async function confirmPower(): Promise<void> {
    if (!confirming) return;
    const { name, plan } = confirming;
    try {
      const res = await run('power', name, 'machine', {}, plan.token);
      const fail = res.failed[0];
      if (fail) toast(t('set.failed', { why: failText(fail) }), { tone: 'error' });
    } catch (err) {
      toast(errorText(err), { tone: 'error' });
    } finally {
      confirming = null;
    }
  }

  // Autostart ----------------------------------------------------------

  const addable = $derived.by(() => {
    const listed = new Set((autostart ?? []).map((e) => e.id));
    return apps.filter((a) => a.source !== 'appimage' && !listed.has(a.id));
  });

  async function addAutostart(): Promise<void> {
    if (!addApp) return;
    const name = apps.find((a) => a.id === addApp)?.name ?? addApp;
    if (await act('autostart', 'add', addApp, {}, t('set.auto.added', { name }), loadAutostart)) addApp = '';
  }

  let density = $state<Density>(loadDensity());

  function setDensity(d: Density): void {
    density = d;
    applyDensity(d);
    saveDensity(d);
  }
</script>

<header>
  <h1>{t('set.title')}</h1>
  <p>{t('set.intro')}</p>
</header>

{#if failed}
  <p class="note error" role="status">{t('set.error')}</p>
{/if}

<div class="sections" aria-busy={busy}>
  <!-- Appearance -->
  <section aria-labelledby="s-look">
    <h2 id="s-look">{t('set.look.title')}</h2>
    <div class="segmented" role="radiogroup" aria-label={t('set.look.density')}>
      {#each ['comfortable', 'compact'] as const as d (d)}
        <button type="button" role="radio" aria-checked={density === d} class:on={density === d} onclick={() => setDensity(d)}>
          {t(d === 'compact' ? 'set.look.compact' : 'set.look.comfortable')}
        </button>
      {/each}
    </div>
    <p class="note">{t('set.look.hint')}</p>
  </section>

  <!-- Network -->
  <section aria-labelledby="s-net">
    <h2 id="s-net">{t('set.net.title')}</h2>
    {#if net === null}
      <p class="note">{t('set.loading')}</p>
    {:else if !net.available}
      <p class="note">{t('set.net.unavailable')}</p>
    {:else}
      {#if wifiDevices.length > 0}
        <div class="row">
          <label class="switch">
            <input
              type="checkbox"
              checked={net.wifi_enabled}
              disabled={!net.wifi_hardware}
              onchange={(e) => act('wifi', 'radio', e.currentTarget.checked ? 'on' : 'off', {}, '', loadNet)}
            />
            {t('set.net.wifi')}
          </label>
          {#if !net.wifi_hardware}<span class="note">{t('set.net.hardOff')}</span>{/if}
          {#each wifiDevices as d (d.name)}
            <button type="button" class="ghost" disabled={!net.wifi_enabled} onclick={() => act('wifi', 'scan', d.name, {}, t('set.net.scanning'), loadNet)}>
              {t('set.net.scan')}
            </button>
          {/each}
        </div>
        {#if net.wifi_enabled}
          <ul class="list">
            {#each net.access_points as ap (ap.ssid)}
              <li>
                <span class="bars" title={`${ap.strength}%`} aria-label={t('set.net.strength', { n: ap.strength })}>
                  {#each [1, 2, 3, 4] as b (b)}<span class:on={b <= bars(ap.strength)}></span>{/each}
                </span>
                <span class="grow">
                  <span class="name">{ap.ssid}</span>
                  <span class="meta">{t(ap.security === 'open' ? 'set.net.open' : ap.security === 'psk' ? 'set.net.secured' : 'set.net.enterprise')}{#if ap.saved}&nbsp;· {t('set.net.saved')}{/if}</span>
                </span>
                {#if ap.active}
                  <span class="badge">{t('set.net.connected')}</span>
                  <button type="button" class="ghost" onclick={() => apDisconnect(ap)}>{t('set.net.disconnect')}</button>
                {:else if ap.security === 'enterprise' && !ap.saved}
                  <span class="meta">{t('set.net.enterpriseHint')}</span>
                {:else}
                  <button type="button" class="ghost" onclick={() => apConnect(ap)}>{t('set.net.connect')}</button>
                {/if}
              </li>
            {:else}
              <li class="note">{t('set.net.noNetworks')}</li>
            {/each}
          </ul>
          <p class="hint">{t('set.net.secretHint')}</p>
        {/if}
      {/if}
      {#if otherConnections(net.connections).length > 0}
        <h3>{t('set.net.others')}</h3>
        <ul class="list">
          {#each otherConnections(net.connections) as c (c.uuid)}
            <li>
              <span class="grow">
                <span class="name">{c.name}</span>
                <span class="meta">{c.type}</span>
              </span>
              {#if c.active}<span class="badge">{t('set.net.connected')}</span>{/if}
              <button type="button" class="ghost" onclick={() => connToggle(c)}>{t(c.active ? 'set.net.disconnect' : 'set.net.connect')}</button>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </section>

  <!-- Sound -->
  <section aria-labelledby="s-sound">
    <h2 id="s-sound">{t('set.sound.title')}</h2>
    {#if audio === null}
      <p class="note">{t('set.loading')}</p>
    {:else if !audio.available}
      <p class="note">{t('set.sound.unavailable')}</p>
    {:else}
      {#each [{ kind: 'output', label: 'set.sound.outputs' }, { kind: 'input', label: 'set.sound.inputs' }] as const as group (group.kind)}
        <h3>{t(group.label)}</h3>
        <ul class="list">
          {#each audio.devices.filter((d) => d.kind === group.kind) as d (d.id)}
            <li class="audio">
              <label class="pick">
                <input type="radio" name={`default-${group.kind}`} checked={d.default} onchange={() => act('audio', 'default', String(d.id), {}, '', loadAudio)} />
                <span class="name">{d.name}</span>
              </label>
              <input
                type="range"
                min="0"
                max="150"
                step="1"
                value={Math.round(d.volume * 100)}
                aria-label={t('set.sound.volume', { name: d.name })}
                onchange={(e) => setVolume(d, Number(e.currentTarget.value))}
              />
              <span class="num">{Math.round(d.volume * 100)}%</span>
              <button
                type="button"
                class="ghost"
                aria-pressed={d.muted}
                onclick={() => act('audio', 'mute', String(d.id), { muted: d.muted ? '0' : '1' }, '', loadAudio)}
              >
                {t(d.muted ? 'set.sound.unmute' : 'set.sound.mute')}
              </button>
            </li>
          {:else}
            <li class="note">{t('set.sound.none')}</li>
          {/each}
        </ul>
      {/each}
    {/if}
  </section>

  <!-- Display -->
  {#if info && info.backlights.length > 0}
    <section aria-labelledby="s-display">
      <h2 id="s-display">{t('set.display.title')}</h2>
      {#each info.backlights as b (b.name)}
        <div class="row">
          <label class="grow">
            <span class="name">{t('set.display.brightness')}</span>
            <input
              type="range"
              min="1"
              max="100"
              value={b.percent}
              aria-label={t('set.display.brightnessOf', { name: b.name })}
              onchange={(e) => act('system', 'brightness', b.name, { percent: e.currentTarget.value }, '', loadInfo)}
            />
          </label>
          <span class="num">{b.percent}%</span>
        </div>
      {/each}
    </section>
  {/if}

  <!-- Power -->
  <section aria-labelledby="s-power">
    <h2 id="s-power">{t('set.power.title')}</h2>
    {#if power?.profile}
      <div class="segmented" role="radiogroup" aria-label={t('set.power.profile')}>
        {#each power.profile.available ?? [] as p (p)}
          <button type="button" role="radio" aria-checked={power.profile.active === p} class:on={power.profile.active === p} onclick={() => setProfile(p)}>
            {profiles[p] ? t(profiles[p]) : p}
          </button>
        {/each}
      </div>
      {#if power.profile.degraded}<p class="note">{t('card.power.degraded', { reason: power.profile.degraded })}</p>{/if}
    {/if}
    {#if info?.power}
      <div class="row buttons">
        {#each powerActions.filter((a) => info?.power?.[a.name]) as a (a.name)}
          <button type="button" class="ghost" class:danger={a.name === 'poweroff' || a.name === 'reboot'} onclick={() => askPower(a)}>{t(a.label)}</button>
        {/each}
      </div>
    {/if}
  </section>

  <!-- Date & time -->
  {#if info?.time}
    <section aria-labelledby="s-time">
      <h2 id="s-time">{t('set.time.title')}</h2>
      <div class="row">
        <label class="grow">
          <span class="name">{t('set.time.zone')}</span>
          <select value={info.time.timezone} onchange={(e) => act('system', 'timezone', e.currentTarget.value, {}, t('set.saved'), loadInfo)}>
            {#if zones.length === 0}<option value={info.time.timezone}>{info.time.timezone}</option>{/if}
            {#each zoneGroups(zones) as g (g.region)}
              {#if g.region}
                <optgroup label={g.region}>
                  {#each g.zones as z (z)}<option value={z}>{z}</option>{/each}
                </optgroup>
              {:else}
                {#each g.zones as z (z)}<option value={z}>{z}</option>{/each}
              {/if}
            {/each}
          </select>
        </label>
      </div>
      {#if info.time.can_ntp}
        <label class="switch">
          <input type="checkbox" checked={info.time.ntp} onchange={(e) => act('system', 'ntp', e.currentTarget.checked ? 'on' : 'off', {}, '', loadInfo)} />
          {t('set.time.ntp')}
        </label>
        {#if info.time.ntp}<span class="meta">{t(info.time.ntp_synchronized ? 'set.time.synced' : 'set.time.notSynced')}</span>{/if}
      {/if}
    </section>
  {/if}

  <!-- About -->
  {#if info?.hostname}
    <section aria-labelledby="s-about">
      <h2 id="s-about">{t('set.about.title')}</h2>
      <form
        class="row"
        onsubmit={(e) => {
          e.preventDefault();
          void act('system', 'hostname', 'pretty', { value: prettyName.trim() }, t('set.saved'), loadInfo);
        }}
      >
        <label class="grow">
          <span class="name">{t('set.about.pretty')}</span>
          <input type="text" maxlength="64" bind:value={prettyName} />
        </label>
        <button type="submit" class="ghost" disabled={prettyName.trim() === info.hostname.pretty}>{t('set.save')}</button>
      </form>
      <form
        class="row"
        onsubmit={(e) => {
          e.preventDefault();
          if (validHostname(staticName)) void act('system', 'hostname', 'static', { value: staticName }, t('set.saved'), loadInfo);
        }}
      >
        <label class="grow">
          <span class="name">{t('set.about.hostname')}</span>
          <input type="text" maxlength="64" spellcheck="false" autocapitalize="off" bind:value={staticName} aria-invalid={!validHostname(staticName)} />
        </label>
        <button type="submit" class="ghost" disabled={!validHostname(staticName) || staticName === info.hostname.static}>{t('set.save')}</button>
      </form>
      {#if !validHostname(staticName)}<p class="note error">{t('set.about.invalid')}</p>{/if}
    </section>
  {/if}

  <section aria-labelledby="s-hearth">
    <h2 id="s-hearth">{t('set.hearth.title')}</h2>
    <p class="version"><span class="name">{t('set.hearth.version')}</span> <bdi data-testid="version">{hearthVersion}</bdi></p>
  </section>

  <!-- Startup applications -->
  <section aria-labelledby="s-auto" class="wide">
    <h2 id="s-auto">{t('set.auto.title')}</h2>
    {#if autostart === null}
      <p class="note">{t('set.loading')}</p>
    {:else}
      <ul class="list">
        {#each autostart as e (e.id)}
          <li>
            <span class="grow">
              <span class="name">{e.name}</span>
              {#if e.comment}<span class="meta">{e.comment}</span>{/if}
            </span>
            <label class="switch">
              <input
                type="checkbox"
                checked={e.enabled}
                aria-label={t('set.auto.toggle', { name: e.name })}
                onchange={(ev) => act('autostart', 'set', e.id, { enabled: String(ev.currentTarget.checked) }, '', loadAutostart)}
              />
            </label>
          </li>
        {:else}
          <li class="note">{t('set.auto.none')}</li>
        {/each}
      </ul>
      <form
        class="row"
        onsubmit={(e) => {
          e.preventDefault();
          void addAutostart();
        }}
      >
        <select aria-label={t('set.auto.add')} bind:value={addApp}>
          <option value="">{t('set.auto.pick')}</option>
          {#each addable as a (a.id)}<option value={a.id}>{a.name}</option>{/each}
        </select>
        <button type="submit" class="ghost" disabled={!addApp}>{t('set.auto.add')}</button>
      </form>
    {/if}
  </section>
</div>

<ConfirmDialog
  plan={confirming?.plan ?? null}
  title={confirming?.title ?? ''}
  verb={confirming?.verb ?? ''}
  body={confirming?.plan.items[0]?.detail ? t('set.power.inhibited', { who: confirming.plan.items[0].detail }) : t('set.power.body')}
  onconfirm={confirmPower}
  oncancel={() => (confirming = null)}
/>

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
  .sections {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
    gap: var(--space-4);
    align-items: start;
  }
  .sections[aria-busy='true'] {
    cursor: progress;
  }
  section {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    padding: var(--space-4);
    min-width: 0;
  }
  section.wide {
    grid-column: 1 / -1;
  }
  h2 {
    font-size: 15px;
    margin: 0 0 var(--space-3);
  }
  h3 {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin: var(--space-3) 0 var(--space-2);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    flex-wrap: wrap;
    margin-bottom: var(--space-3);
  }
  .grow {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list li {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-2) 0;
    border-top: 1px solid var(--border);
  }
  .list li:first-child {
    border-top: 0;
  }
  .name {
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta,
  .hint,
  .note {
    font-size: 12px;
    color: var(--text-muted);
  }
  .error {
    color: var(--crit);
  }
  .num {
    font-size: 12px;
    font-variant-numeric: tabular-nums;
    min-width: 3.5em;
    text-align: end;
  }
  .badge {
    font-size: 11px;
    color: var(--ok);
  }
  .bars {
    display: inline-flex;
    align-items: flex-end;
    gap: 2px;
    height: 14px;
  }
  .bars span {
    width: 3px;
    background: var(--border);
    border-radius: 1px;
  }
  .bars span:nth-child(1) {
    height: 25%;
  }
  .bars span:nth-child(2) {
    height: 50%;
  }
  .bars span:nth-child(3) {
    height: 75%;
  }
  .bars span:nth-child(4) {
    height: 100%;
  }
  .bars span.on {
    background: var(--accent);
  }
  .audio input[type='range'] {
    width: 120px;
  }
  .pick {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .switch {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    font-size: 13px;
  }
  input[type='text'],
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
    max-width: 100%;
  }
  input[aria-invalid='true'] {
    border-color: var(--crit);
  }
  .ghost {
    font: inherit;
    font-size: 12px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-3);
    cursor: pointer;
    white-space: nowrap;
  }
  .ghost:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .ghost:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .ghost.danger:hover {
    border-color: var(--crit);
    color: var(--crit);
  }
  .buttons {
    margin-top: var(--space-3);
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
  @media (max-width: 480px) {
    .sections {
      grid-template-columns: 1fr;
    }
  }
</style>

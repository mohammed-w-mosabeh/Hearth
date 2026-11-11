<script lang="ts">
  import { api } from '../lib/api';
  import { conditionText, metricLabel, type Metric } from '../lib/alerts';
  import type { App } from '../lib/apps';
  import {
    CHANNELS,
    CHECKS,
    CONDITIONS,
    METRICS,
    NEW_TRIGGERS,
    PLACEHOLDERS,
    STEP_KINDS,
    WEEKDAYS,
    actionKey,
    blankCondition,
    blankStep,
    blankTrigger,
    body,
    catalogEntry,
    moved,
    offered,
    triggerWords,
    type ActionInfo,
    type CatalogEntry,
    type CheckWhat,
    type ConditionKind,
    type Draft,
    type Step,
    type TriggerKind,
  } from '../lib/automations';
  import type { Snapshot } from '../lib/docker';
  import { failure } from '../lib/errors';
  import { locale, t, type MessageKey } from '../lib/i18n';
  import { candidates, type Connection, type Mode, type Sources } from '../lib/modes';
  import { href } from '../lib/router.svelte';
  import type { ServicesMsg, Unit } from '../lib/services';
  import type { StackInfo } from '../lib/stacks';
  import { toast } from '../lib/toast.svelte';

  // The automation editor (ROADMAP 12.9): When / If / Then with pickers
  // only. Each step shows how it would go now, from the server's own check
  // (runs by itself, or waits for your approval); the dry run sits beside
  // Save. An alert rule's trigger is changed on the Alerts page.
  let { draft, onclose, onsaved }: { draft: Draft | null; onclose: () => void; onsaved: () => void } = $props();

  let dialog: HTMLDialogElement;
  let d = $state<Draft | null>(null);
  let server = $state.raw<ActionInfo[]>([]);
  let units = $state.raw<Unit[]>([]);
  let docker = $state.raw<Snapshot | null>(null);
  let apps = $state.raw<App[]>([]);
  let stacks = $state.raw<string[]>([]);
  let connections = $state.raw<Connection[]>([]);
  let modes = $state.raw<Mode[]>([]);
  let metrics = $state.raw<Metric[]>([]);

  $effect(() => {
    if (!draft) {
      d = null;
      if (dialog.open) dialog.close();
      return;
    }
    d = $state.snapshot(draft); // a plain copy: the list's object is never changed
    checked = null;
    dry = null;
    dryFor = '';
    if (!dialog.open) dialog.showModal();
    api<{ actions: ActionInfo[] }>('/automations/actions').then((r) => (server = r.actions), () => {});
    api<ServicesMsg>('/services').then((m) => (units = m.kind === 'list' ? m.units : []), () => {});
    api<Snapshot>('/docker').then((s) => (docker = s), () => {});
    api<{ apps: App[] }>('/apps').then((r) => (apps = r.apps), () => {});
    api<StackInfo[]>('/stacks').then((r) => (stacks = r.map((s) => s.name)), () => {});
    api<{ connections: Connection[] }>('/system/network').then((r) => (connections = r.connections ?? []), () => {});
    api<{ modes: Mode[] }>('/modes').then((r) => (modes = r.modes), () => {});
    api<{ metrics: Metric[] }>('/alerts/metrics').then((r) => (metrics = r.metrics), () => {});
  });

  const src = $derived<Sources>({ units, docker, apps, stacks, connections });
  const actionList = $derived(offered(server));
  const isAlert = $derived(d?.spec.trigger.kind === 'alert');

  // ---- Live check: the save-time checks, and each step as it would go now.
  interface Checked {
    error?: string;
    steps: { outcome: string; branch?: string; reason?: string; detail?: string }[];
  }
  let checked = $state.raw<Checked | null>(null);
  const specJSON = $derived(d ? JSON.stringify(d.spec) : '');
  $effect(() => {
    const s = specJSON;
    if (!s) return;
    const timer = setTimeout(() => {
      api<Checked>('/automations/check', { method: 'POST', body: JSON.stringify({ spec: JSON.parse(s) }) }).then(
        (r) => {
          if (s === specJSON) checked = r;
        },
        (err) => (checked = { error: failure(err, 'action.failed'), steps: [] }),
      );
    }, 400);
    return () => clearTimeout(timer);
  });

  /** Each step's outcome by its place: "0", "1.then.0", in the server's order. */
  const outcomes = $derived.by(() => {
    const out: Record<string, Checked['steps'][number]> = {};
    const list = checked?.steps ?? [];
    let i = 0;
    for (const [n, st] of (d?.spec.then ?? []).entries()) {
      const top = list[i++];
      if (top) out[String(n)] = top;
      if (st.step === 'check') {
        for (const [b, sub] of (st.then ?? []).entries()) {
          const r = list[i++];
          if (r && sub) out[`${n}.then.${b}`] = r;
        }
        for (const [b, sub] of (st.else ?? []).entries()) {
          const r = list[i++];
          if (r && sub) out[`${n}.else.${b}`] = r;
        }
      }
    }
    return out;
  });

  // ---- Dry run.
  interface DryRun {
    total: number;
    runs: number;
    truncated?: boolean;
    trigger: { state: string; notes: string[]; since?: string; for_s?: number };
    conditions: { kind: string; state: string; notes: string[] }[];
    firings: { time: string; instance?: string; value?: number; outcome: string; reason?: string; missing?: string[] }[];
  }
  let dry = $state.raw<DryRun | null>(null);
  let dryFor = $state('');
  let dryBusy = $state(false);
  let showAll = $state(false);

  async function dryRun(): Promise<void> {
    if (!d) return;
    dryBusy = true;
    const s = specJSON;
    try {
      dry = await api<DryRun>('/automations/dry-run', { method: 'POST', body: JSON.stringify({ ...(d.id ? { id: d.id } : {}), spec: JSON.parse(s) }) });
      dryFor = s;
      showAll = false;
    } catch (err) {
      toast(failure(err, 'auto.dry.failed'), { tone: 'error' });
    } finally {
      dryBusy = false;
    }
  }

  // ---- Save.
  let saving = $state(false);
  const canSave = $derived(!!d && d.name.trim() !== '' && checked !== null && !checked.error && !saving);
  const dryFirst = $derived(dryFor !== specJSON);

  async function save(): Promise<void> {
    if (!d) return;
    saving = true;
    try {
      await api(d.id ? `/automations/${d.id}` : '/automations', { method: d.id ? 'PUT' : 'POST', body: body($state.snapshot(d)) });
      toast(t('auto.saved', { s: d.name }));
      onsaved();
    } catch (err) {
      toast(failure(err, 'action.failed'), { tone: 'error' });
    } finally {
      saving = false;
    }
  }

  // ---- Editing helpers.
  function setTrigger(kind: TriggerKind): void {
    if (d) d.spec.trigger = blankTrigger(kind);
  }

  function addCondition(kind: ConditionKind): void {
    if (!d) return;
    d.spec.if ??= { match: 'all', conditions: [] };
    d.spec.if.conditions.push(blankCondition(kind));
  }

  function removeCondition(i: number): void {
    if (!d?.spec.if) return;
    d.spec.if.conditions.splice(i, 1);
    if (d.spec.if.conditions.length === 0) delete d.spec.if;
  }

  function setAction(st: Step, key: string): void {
    st.action = key;
    st.target = catalogEntry(key)?.options?.[0] ?? '';
    delete st.params;
  }

  function targets(entry: CatalogEntry | undefined): { value: string; label: string }[] {
    if (!entry) return [];
    switch (entry.picker) {
      case 'fixed':
        return (entry.options ?? []).map((o) => ({ value: o, label: t(`auto.target.${o}` as MessageKey) }));
      default:
        return candidates(entry.picker, src, false);
    }
  }

  function toggleChannel(st: Step, c: string, on: boolean): void {
    const now = (st.channels ?? []).filter((x) => x !== 'all' && x !== c);
    st.channels = on ? [...now, c] : now;
  }

  function insertHole(st: Step, hole: string): void {
    st.text = `${st.text ?? ''}{${hole}}`.slice(0, 300);
  }

  const metricOf = (id: string | undefined) => metrics.find((m) => m.id === id);
  const outcomeKey = (o: string): MessageKey => `auto.out.${o === 'unattended' || o === 'approval' || o === 'refused' ? o : 'fail'}`;
  const when = (iso: string) => new Date(iso).toLocaleString(locale());
  const connectionsOf = (type: 'wifi' | 'vpn') => connections.filter((c) => (type === 'vpn' ? c.type.includes('vpn') || c.type.includes('wireguard') : c.type.includes('wireless')));
  const condName = (k: string): string => ((CONDITIONS as readonly string[]).includes(k) ? t(`auto.cond.${k as ConditionKind}`) : k);

  function triggerText(): string {
    if (!d) return '';
    const w = triggerWords(d.spec.trigger, {
      connection: (u) => connections.find((c) => c.uuid === u)?.name ?? u,
      metric: (m) => metricLabel(metricOf(m), m),
      alert: (tr) => conditionText({ metric: tr.metric ?? '', target: tr.target ?? '', op: tr.op === '<' ? '<' : '>', threshold: tr.threshold ?? 0, for_s: tr.for_s ?? 0 }, metricOf(tr.metric ?? '')),
    });
    return t(w.key, w.values);
  }

  function noteText(n: string): string {
    if (!dry) return n;
    return t(`auto.dry.note.${n}` as MessageKey, { s: dry.trigger.for_s ?? 0, time: dry.trigger.since ? when(dry.trigger.since) : '' });
  }

  function firingText(f: DryRun['firings'][number]): string {
    if (f.outcome === 'run') return t('auto.dry.run');
    if (f.outcome === 'unknown') return t('auto.dry.unknown', { s: (f.missing ?? []).map(condName).join(', ') });
    return t(`auto.dry.why.${f.reason ?? 'conditions'}` as MessageKey);
  }
</script>

{#snippet stepEditor(st: Step, path: string, nested: boolean, remove: () => void, up: () => void, down: () => void)}
  {@const o = outcomes[path]}
  <li class="step">
    <div class="steprow">
      <select aria-label={t('auto.f.stepKind')} value={st.step} disabled>
        {#each STEP_KINDS as k (k)}<option value={k}>{t(`auto.stepKind.${k}`)}</option>{/each}
      </select>
      {#if o}
        <span class="out {o.outcome}">{t(outcomeKey(o.outcome))}</span>
        {#if o.reason}<span class="sub">{t(`auto.out.why.${o.reason}` as MessageKey)}</span>{/if}
        {#if o.detail}<span class="sub">{o.detail}</span>{/if}
      {/if}
      <span class="grow"></span>
      <button type="button" class="mini" aria-label={t('auto.up')} onclick={up}>↑</button>
      <button type="button" class="mini" aria-label={t('auto.down')} onclick={down}>↓</button>
      <button type="button" class="mini" aria-label={t('auto.remove')} onclick={remove}>×</button>
    </div>
    {#if st.step === 'action'}
      {@const entry = catalogEntry(st.action ?? '')}
      <div class="fields">
        <label>
          {t('auto.f.action')}
          {#if entry || !st.action}
            <select value={st.action} onchange={(e) => setAction(st, e.currentTarget.value)}>
              {#if !st.action}<option value="" disabled>{t('auto.pick')}</option>{/if}
              {#each actionList as a (a.key)}<option value={a.key}>{t(actionKey(a.key))}</option>{/each}
            </select>
          {:else}
            <span class="fixed">{st.action}</span>
          {/if}
        </label>
        <label>
          {t('auto.f.target')}
          {#if entry}
            <select bind:value={st.target}>
              {#if !st.target}<option value="" disabled>{t('auto.pick')}</option>{/if}
              {#each targets(entry) as c (c.value)}<option value={c.value}>{c.value === c.label ? c.value : `${c.value} — ${c.label}`}</option>{/each}
              {#if st.target && !targets(entry).some((c) => c.value === st.target)}<option value={st.target}>{st.target}</option>{/if}
            </select>
          {:else}
            <span class="fixed">{st.target ?? ''}</span>
          {/if}
        </label>
        {#if st.params && Object.keys(st.params).length > 0 && !entry}
          <span class="sub">{Object.entries(st.params).map(([k, v]) => `${k}=${v}`).join(', ')}</span>
        {/if}
      </div>
    {:else if st.step === 'notify'}
      <div class="fields">
        <fieldset class="inline">
          <legend>{t('auto.f.channels')}</legend>
          {#if st.channels?.includes('all')}
            <span class="sub">{t('auto.channel.all')}</span>
          {:else}
            {#each CHANNELS as c (c)}
              <label><input type="checkbox" checked={st.channels?.includes(c)} onchange={(e) => toggleChannel(st, c, e.currentTarget.checked)} /> {t(`auto.channel.${c}`)}</label>
            {/each}
          {/if}
        </fieldset>
        <label>
          {t('auto.f.severity')}
          <select value={st.severity ?? 'info'} onchange={(e) => (st.severity = e.currentTarget.value === 'info' ? undefined : e.currentTarget.value)}>
            {#each ['info', 'warning', 'critical'] as s (s)}<option value={s}>{t(`auto.severity.${s as 'info'}`)}</option>{/each}
          </select>
        </label>
        <label class="wide">
          {t('auto.f.text')}
          <textarea rows="2" maxlength="300" bind:value={st.text}></textarea>
        </label>
        <div class="holes" role="group" aria-label={t('auto.f.holes')}>
          {#each PLACEHOLDERS as h (h)}<button type="button" class="mini" onclick={() => insertHole(st, h)}>{`{${h}}`}</button>{/each}
        </div>
      </div>
    {:else if st.step === 'wait'}
      <div class="fields">
        <label>{t('auto.f.seconds')} <input type="number" min="1" max="600" bind:value={st.seconds} /></label>
      </div>
    {:else if st.step === 'check' && !nested}
      {@const what = (st.what ?? 'unit_state') as CheckWhat}
      {@const kind = what === 'unit_state' ? 'unit' : 'container'}
      <div class="fields">
        <label>
          {t('auto.f.check')}
          <select
            value={what}
            onchange={(e) => {
              st.what = e.currentTarget.value;
              st.expect = CHECKS[st.what as CheckWhat][0];
              st.target = '';
            }}
          >
            {#each Object.keys(CHECKS) as w (w)}<option value={w}>{t(`auto.check.${w as CheckWhat}`)}</option>{/each}
          </select>
        </label>
        <label>
          {t('auto.f.target')}
          <select bind:value={st.target}>
            <option value="" disabled>{t('auto.pick')}</option>
            {#each candidates(kind, src, false) as c (c.value)}<option value={c.value}>{c.value}</option>{/each}
            {#if st.target && !candidates(kind, src, false).some((c) => c.value === st.target)}<option value={st.target}>{st.target}</option>{/if}
          </select>
        </label>
        <label>
          {t('auto.f.expect')}
          <select bind:value={st.expect}>
            {#each CHECKS[what] as x (x)}<option value={x}>{t(`auto.state.${x}`)}</option>{/each}
          </select>
        </label>
      </div>
      {#each ['then', 'else'] as const as branch (branch)}
        {@const list = st[branch] ?? []}
        <div class="branch">
          <h5>{t(`auto.branch.${branch}`)}</h5>
          <ol>
            {#each list as sub, i (i)}
              {@render stepEditor(
                sub,
                `${path}.${branch}.${i}`,
                true,
                () => (st[branch] = list.filter((_, j) => j !== i)),
                () => (st[branch] = moved(list, i, -1)),
                () => (st[branch] = moved(list, i, 1)),
              )}
            {/each}
          </ol>
          <div class="adds">
            {#each STEP_KINDS.filter((k) => k !== 'check') as k (k)}
              <button type="button" class="mini" onclick={() => (st[branch] = [...list, blankStep(k, actionList[0])])}>+ {t(`auto.stepKind.${k}`)}</button>
            {/each}
          </div>
        </div>
      {/each}
    {/if}
    <div class="fields">
      <label class="wide">{t('auto.f.label')} <input type="text" maxlength="80" value={st.label ?? ''} oninput={(e) => (st.label = e.currentTarget.value || undefined)} /></label>
    </div>
  </li>
{/snippet}

<dialog bind:this={dialog} aria-labelledby="auto-editor-title" onclose={() => draft && onclose()}>
  {#if d}
    {@const tr = d.spec.trigger}
    <header>
      <h2 id="auto-editor-title">{d.id ? t('auto.edit.title') : t('auto.new.title')}</h2>
      <button type="button" class="x" aria-label={t('auto.close')} onclick={() => onclose()}>×</button>
    </header>
    <div class="body">
      <label class="wide">{t('auto.f.name')} <input type="text" maxlength="80" bind:value={d.name} required /></label>
      <label class="switch"><input type="checkbox" bind:checked={d.enabled} /> {t('auto.f.enabled')}</label>

      <section aria-labelledby="sec-when">
        <h3 id="sec-when">{t('auto.sec.when')}</h3>
        {#if isAlert}
          <p>{triggerText()}</p>
          <p class="note">{t('auto.alertTrigger')} <a href={href('alerts')}>{t('auto.openAlerts')}</a></p>
        {:else}
          <div class="fields">
            <label>
              {t('auto.f.trigger')}
              <select value={tr.kind} onchange={(e) => setTrigger(e.currentTarget.value as TriggerKind)}>
                {#each NEW_TRIGGERS as k (k)}<option value={k}>{t(`auto.kind.${k}`)}</option>{/each}
                {#if !(NEW_TRIGGERS as readonly string[]).includes(tr.kind)}<option value={tr.kind}>{triggerText()}</option>{/if}
              </select>
            </label>
            {#if tr.kind === 'schedule'}
              <label>{t('auto.f.calendar')} <input type="text" class="mono" maxlength="128" bind:value={tr.calendar} /></label>
              <span class="sub">{t('auto.calendarHint')}</span>
            {:else if tr.kind === 'metric'}
              {@const m = metricOf(tr.metric)}
              <label>
                {t('auto.f.metric')}
                <select bind:value={tr.metric}>
                  {#each METRICS as id (id)}<option value={id}>{metricLabel(metricOf(id), id)}</option>{/each}
                </select>
              </label>
              {#if m?.instance}
                <label>
                  {t('auto.f.instance')}
                  <select value={tr.target ?? ''} onchange={(e) => (tr.target = e.currentTarget.value || undefined)}>
                    <option value="">{t('auto.any')}</option>
                    {#each m.instances as i (i)}<option value={i}>{i}</option>{/each}
                  </select>
                </label>
              {/if}
              <label>
                {t('auto.f.op')}
                <select bind:value={tr.op}><option value=">">{t('auto.op.above')}</option><option value="<">{t('auto.op.below')}</option></select>
              </label>
              <label>{t('auto.f.threshold')} <input type="number" min={m?.min ?? 0} max={m?.max ?? 150} bind:value={tr.threshold} /></label>
              <label>{t('auto.f.forS')} <input type="number" min="0" max="86400" bind:value={tr.for_s} /></label>
            {:else if tr.kind === 'container'}
              <label>
                {t('auto.f.event')}
                <select bind:value={tr.event}>{#each ['died', 'unhealthy', 'oom'] as e (e)}<option value={e}>{t(`auto.event.${e as 'died'}`)}</option>{/each}</select>
              </label>
              <label>
                {t('auto.f.container')}
                <select value={tr.target ?? ''} onchange={(e) => (tr.target = e.currentTarget.value || undefined)}>
                  <option value="">{t('auto.any')}</option>
                  {#each candidates('container', src, false) as c (c.value)}<option value={c.value}>{c.value}</option>{/each}
                </select>
              </label>
            {:else if tr.kind === 'unit_failed'}
              <label>
                {t('auto.f.scope')}
                <select value={tr.scope ?? ''} onchange={(e) => (tr.scope = e.currentTarget.value || undefined)}>
                  <option value="">{t('auto.anyScope')}</option><option value="user">{t('auto.scope.user')}</option><option value="system">{t('auto.scope.system')}</option>
                </select>
              </label>
              <label>
                {t('auto.f.unit')}
                <select value={tr.target ?? ''} onchange={(e) => (tr.target = e.currentTarget.value || undefined)}>
                  <option value="">{t('auto.any')}</option>
                  {#each units.filter((u) => !tr.scope || u.scope === tr.scope) as u (u.scope + u.name)}<option value={u.name}>{u.name}</option>{/each}
                  {#if tr.target && !units.some((u) => u.name === tr.target)}<option value={tr.target}>{tr.target}</option>{/if}
                </select>
              </label>
            {:else if tr.kind === 'image_update'}
              <label>
                {t('auto.f.stack')}
                <select value={tr.target ?? ''} onchange={(e) => (tr.target = e.currentTarget.value || undefined)}>
                  <option value="">{t('auto.any')}</option>
                  {#each stacks as s (s)}<option value={s}>{s}</option>{/each}
                </select>
              </label>
            {:else if tr.kind === 'ssh_failed'}
              <label>{t('auto.f.count')} <input type="number" min="3" max="1000" bind:value={tr.count} /></label>
            {:else if tr.kind === 'power'}
              <label>
                {t('auto.f.event')}
                <select bind:value={tr.event}><option value="unplugged">{t('auto.event.unplugged')}</option><option value="plugged">{t('auto.event.plugged')}</option></select>
              </label>
            {:else if tr.kind === 'wifi' || tr.kind === 'vpn'}
              <label>
                {t('auto.f.connection')}
                <select bind:value={tr.uuid}>
                  <option value="" disabled>{t('auto.pick')}</option>
                  {#each connectionsOf(tr.kind) as c (c.uuid)}<option value={c.uuid}>{c.name}</option>{/each}
                  {#if tr.uuid && !connections.some((c) => c.uuid === tr.uuid)}<option value={tr.uuid}>{tr.uuid}</option>{/if}
                </select>
              </label>
              <label>
                {t('auto.f.state')}
                <select bind:value={tr.state}><option value="up">{t('auto.state.up')}</option><option value="down">{t('auto.state.down')}</option></select>
              </label>
            {:else if tr.kind === 'internet'}
              <label>
                {t('auto.f.state')}
                <select bind:value={tr.state}><option value="down">{t('auto.state.down')}</option><option value="back">{t('auto.state.back')}</option></select>
              </label>
            {:else if tr.kind === 'idle'}
              <label>{t('auto.f.minutes')} <input type="number" min="5" max="240" bind:value={tr.minutes} /></label>
            {:else if tr.kind === 'display'}
              <label>
                {t('auto.f.event')}
                <select bind:value={tr.event}><option value="connected">{t('auto.event.connected')}</option><option value="disconnected">{t('auto.event.disconnected')}</option></select>
              </label>
            {:else if tr.kind === 'app'}
              <label>
                {t('auto.f.app')}
                <select
                  value={tr.game ? '' : (tr.app ?? '')}
                  onchange={(e) => {
                    const v = e.currentTarget.value;
                    if (v) {
                      tr.app = v;
                      delete tr.game;
                    } else {
                      tr.game = true;
                      delete tr.app;
                    }
                  }}
                >
                  <option value="">{t('auto.anyGame')}</option>
                  {#each candidates('app', src, false) as c (c.value)}<option value={c.value}>{c.label}</option>{/each}
                </select>
              </label>
              <label>
                {t('auto.f.state')}
                <select bind:value={tr.state}><option value="started">{t('auto.state.started')}</option><option value="stopped">{t('auto.state.stopped')}</option></select>
              </label>
            {/if}
          </div>
          <p class="note">{t('auto.noFolders')}</p>
        {/if}
      </section>

      <section aria-labelledby="sec-if">
        <h3 id="sec-if">{t('auto.sec.if')}</h3>
        {#if d.spec.if}
          {@const cs = d.spec.if}
          <label>
            {t('auto.f.match')}
            <select bind:value={cs.match}><option value="all">{t('auto.match.all')}</option><option value="any">{t('auto.match.any')}</option></select>
          </label>
          <ol>
            {#each cs.conditions as c, i (i)}
              <li class="step">
                <div class="steprow">
                  <strong>{condName(c.kind)}</strong>
                  <span class="grow"></span>
                  <button type="button" class="mini" aria-label={t('auto.remove')} onclick={() => removeCondition(i)}>×</button>
                </div>
                <div class="fields">
                  {#if c.kind === 'time'}
                    <label>{t('auto.f.from')} <input type="time" bind:value={c.from} /></label>
                    <label>{t('auto.f.to')} <input type="time" bind:value={c.to} /></label>
                  {:else if c.kind === 'weekday'}
                    <fieldset class="inline">
                      <legend class="visually-hidden">{t('auto.cond.weekday')}</legend>
                      {#each WEEKDAYS as day (day)}
                        <label
                          ><input
                            type="checkbox"
                            checked={c.days?.includes(day)}
                            onchange={(e) => (c.days = e.currentTarget.checked ? [...(c.days ?? []), day] : (c.days ?? []).filter((x) => x !== day))}
                          />
                          {t(`auto.day.${day}`)}</label
                        >
                      {/each}
                    </fieldset>
                  {:else if c.kind === 'power'}
                    <select aria-label={t('auto.cond.power')} bind:value={c.on}><option value="ac">{t('auto.on.ac')}</option><option value="battery">{t('auto.on.battery')}</option></select>
                  {:else if c.kind === 'battery'}
                    <select aria-label={t('auto.f.op')} bind:value={c.op}><option value="below">{t('auto.op.below')}</option><option value="above">{t('auto.op.above')}</option></select>
                    <label>{t('auto.f.percent')} <input type="number" min="1" max="99" bind:value={c.percent} /></label>
                  {:else if c.kind === 'wifi' || c.kind === 'mode'}
                    <select aria-label={condName(c.kind)} value={c.kind === 'wifi' ? c.uuid : c.mode} onchange={(e) => (c.kind === 'wifi' ? (c.uuid = e.currentTarget.value) : (c.mode = e.currentTarget.value))}>
                      <option value="" disabled>{t('auto.pick')}</option>
                      {#if c.kind === 'wifi'}
                        {#each connectionsOf('wifi') as x (x.uuid)}<option value={x.uuid}>{x.name}</option>{/each}
                        {#if c.uuid && !connections.some((x) => x.uuid === c.uuid)}<option value={c.uuid}>{c.uuid}</option>{/if}
                      {:else}
                        {#each modes as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
                        {#if c.mode && !modes.some((m) => m.id === c.mode)}<option value={c.mode}>{c.mode}</option>{/if}
                      {/if}
                    </select>
                    <select aria-label={t('auto.f.is')} value={c.is ? 'yes' : 'no'} onchange={(e) => (c.is = e.currentTarget.value === 'yes' ? true : undefined)}>
                      <option value="yes">{t(c.kind === 'wifi' ? 'auto.is.connected' : 'auto.is.active')}</option>
                      <option value="no">{t(c.kind === 'wifi' ? 'auto.is.notConnected' : 'auto.is.notActive')}</option>
                    </select>
                  {:else if c.kind === 'game'}
                    <select aria-label={t('auto.cond.game')} value={c.is ? 'yes' : 'no'} onchange={(e) => (c.is = e.currentTarget.value === 'yes' ? true : undefined)}>
                      <option value="yes">{t('auto.is.playing')}</option>
                      <option value="no">{t('auto.is.notPlaying')}</option>
                    </select>
                  {:else if c.kind === 'container'}
                    <select aria-label={t('auto.f.container')} bind:value={c.target}>
                      <option value="" disabled>{t('auto.pick')}</option>
                      {#each candidates('container', src, false) as x (x.value)}<option value={x.value}>{x.value}</option>{/each}
                      {#if c.target && !candidates('container', src, false).some((x) => x.value === c.target)}<option value={c.target}>{c.target}</option>{/if}
                    </select>
                    <select aria-label={t('auto.f.state')} bind:value={c.state}>{#each ['running', 'stopped', 'healthy', 'unhealthy'] as s (s)}<option value={s}>{t(`auto.state.${s as 'running'}`)}</option>{/each}</select>
                  {:else if c.kind === 'unit'}
                    <select
                      aria-label={t('auto.f.unit')}
                      value={c.scope && c.target ? `${c.scope}/${c.target}` : ''}
                      onchange={(e) => {
                        const [scope, ...name] = e.currentTarget.value.split('/');
                        c.scope = scope;
                        c.target = name.join('/');
                      }}
                    >
                      <option value="" disabled>{t('auto.pick')}</option>
                      {#each candidates('unit', src, false) as x (x.value)}<option value={x.value}>{x.value}</option>{/each}
                      {#if c.scope && c.target && !candidates('unit', src, false).some((x) => x.value === `${c.scope}/${c.target}`)}<option value={`${c.scope}/${c.target}`}>{`${c.scope}/${c.target}`}</option>{/if}
                    </select>
                    <select aria-label={t('auto.f.state')} bind:value={c.state}>{#each ['active', 'inactive', 'failed'] as s (s)}<option value={s}>{t(`auto.state.${s as 'active'}`)}</option>{/each}</select>
                  {:else}
                    <span class="sub">{JSON.stringify(c)}</span>
                  {/if}
                </div>
              </li>
            {/each}
          </ol>
        {:else}
          <p class="note">{t('auto.noConditions')}</p>
        {/if}
        <div class="adds">
          {#each CONDITIONS as k (k)}<button type="button" class="mini" onclick={() => addCondition(k)}>+ {t(`auto.cond.${k}`)}</button>{/each}
        </div>
      </section>

      <section aria-labelledby="sec-then">
        <h3 id="sec-then">{t('auto.sec.then')}</h3>
        <p class="note">{t('auto.thenNote')}</p>
        <ol>
          {#each d.spec.then as st, i (i)}
            {@render stepEditor(
              st,
              String(i),
              false,
              () => d && d.spec.then.splice(i, 1),
              () => d && (d.spec.then = moved(d.spec.then, i, -1)),
              () => d && (d.spec.then = moved(d.spec.then, i, 1)),
            )}
          {/each}
        </ol>
        <div class="adds">
          {#each STEP_KINDS as k (k)}<button type="button" class="mini" onclick={() => d && d.spec.then.push(blankStep(k, actionList[0]))}>+ {t(`auto.stepKind.${k}`)}</button>{/each}
        </div>
      </section>

      <section aria-labelledby="sec-options">
        <h3 id="sec-options">{t('auto.sec.options')}</h3>
        <div class="fields">
          <label>{t('auto.f.cooldown')} <input type="number" min="60" max="86400" bind:value={d.spec.options.cooldown_s} /></label>
          <label>{t('auto.f.perHour')} <input type="number" min="1" max="60" bind:value={d.spec.options.max_per_hour} /></label>
          <label
            >{t('auto.f.quietFrom')}
            <input type="time" value={d.spec.options.quiet_from ?? ''} oninput={(e) => d && (d.spec.options.quiet_from = e.currentTarget.value || undefined)} /></label
          >
          <label
            >{t('auto.f.quietTo')}
            <input type="time" value={d.spec.options.quiet_to ?? ''} oninput={(e) => d && (d.spec.options.quiet_to = e.currentTarget.value || undefined)} /></label
          >
          {#if tr.kind === 'schedule'}
            <label class="switch"
              ><input type="checkbox" checked={d.spec.options.run_if_missed ?? false} onchange={(e) => d && (d.spec.options.run_if_missed = e.currentTarget.checked || undefined)} />
              {t('auto.f.runIfMissed')}</label
            >
          {/if}
        </div>
      </section>

      {#if dry}
        <section class="dry" aria-labelledby="sec-dry" aria-live="polite">
          <h3 id="sec-dry">{t('auto.dry.title')}</h3>
          {#if dry.trigger.state === 'none'}
            <p>{t('auto.dry.noHistory')}</p>
          {:else}
            <p>{t('auto.dry.summary', { n: dry.total, m: dry.runs })}</p>
          {/if}
          {#each dry.trigger.notes as n (n)}<p class="sub">{noteText(n)}</p>{/each}
          {#if dry.truncated}<p class="sub">{t('auto.dry.truncated')}</p>{/if}
          {#each dry.conditions.filter((c) => c.state === 'none') as c (c.kind)}<p class="sub">{t('auto.dry.condNone', { s: condName(c.kind) })}</p>{/each}
          {#if dry.firings.length > 0}
            <ol class="firings">
              {#each showAll ? dry.firings : dry.firings.slice(0, 20) as f, i (i)}
                <li class={f.outcome}>
                  <time datetime={f.time}>{when(f.time)}</time>
                  {#if f.instance}<span class="mono">{f.instance}</span>{/if}
                  <span>{firingText(f)}</span>
                </li>
              {/each}
            </ol>
            {#if !showAll && dry.firings.length > 20}<button type="button" class="mini" onclick={() => (showAll = true)}>{t('auto.dry.more', { n: dry.firings.length - 20 })}</button>{/if}
            {#if dry.total > dry.firings.length}<p class="sub">{t('auto.dry.listed', { n: dry.firings.length, m: dry.total })}</p>{/if}
          {/if}
        </section>
      {/if}
    </div>
    <footer>
      {#if checked?.error}<p class="note error" role="status">{checked.error}</p>{/if}
      <div class="row">
        <button type="button" class="btn" class:primary={dryFirst} onclick={dryRun} disabled={dryBusy || !!checked?.error || checked === null} aria-busy={dryBusy}>{dryBusy ? t('auto.dry.running') : t('auto.dry.button')}</button>
        <button type="button" class="btn" class:primary={!dryFirst} onclick={save} disabled={!canSave} aria-busy={saving}>{t('auto.save')}</button>
        <button type="button" class="btn" onclick={() => onclose()}>{t('auto.cancel')}</button>
      </div>
    </footer>
  {/if}
</dialog>

<style>
  dialog {
    width: min(900px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    padding: var(--space-4);
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  dialog[open] {
    display: flex;
    flex-direction: column;
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 35%);
  }
  header,
  .steprow {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  header {
    justify-content: space-between;
    margin-bottom: var(--space-2);
  }
  h2 {
    font-size: 17px;
  }
  .x {
    font: inherit;
    font-size: 20px;
    line-height: 1;
    color: var(--text-muted);
    background: none;
    border: 0;
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
  }
  h3 {
    font-size: 14px;
    margin: var(--space-3) 0 var(--space-2);
  }
  h5 {
    font-size: 12px;
    margin: var(--space-2) 0 var(--space-1);
    color: var(--text-muted);
  }
  .body {
    overflow-y: auto;
    flex: 1;
    min-height: 0;
    font-size: 13px;
  }
  section {
    border-top: 1px solid var(--border);
    margin-top: var(--space-3);
  }
  .fields {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2) var(--space-3);
    align-items: flex-end;
    margin: var(--space-1) 0;
  }
  label {
    display: inline-flex;
    flex-direction: column;
    gap: 2px;
    color: var(--text-muted);
    font-size: 12px;
  }
  label.wide {
    display: flex;
    flex-basis: 100%;
  }
  label.switch {
    flex-direction: row;
    align-items: center;
    gap: var(--space-2);
    color: var(--text);
    margin: var(--space-2) 0;
  }
  fieldset.inline {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  fieldset.inline label {
    flex-direction: row;
    align-items: center;
    gap: 4px;
    color: var(--text);
  }
  legend {
    font-size: 12px;
    color: var(--text-muted);
    padding: 0;
    margin-bottom: 2px;
  }
  input[type='text'],
  input[type='number'],
  input[type='time'],
  textarea,
  select {
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-1) var(--space-2);
    max-width: 100%;
  }
  input[type='number'] {
    width: 7em;
  }
  textarea {
    width: 100%;
    resize: vertical;
  }
  .mono,
  .fixed {
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .fixed {
    color: var(--text);
  }
  ol {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .step {
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: var(--space-2);
    margin-bottom: var(--space-2);
  }
  .branch {
    margin-inline-start: var(--space-3);
  }
  .grow {
    flex: 1;
  }
  .out {
    font-size: 12px;
    padding: 0 var(--space-2);
    border-radius: 999px;
    border: 1px solid currentColor;
    white-space: nowrap;
  }
  .out.unattended {
    color: var(--ok);
  }
  .out.approval {
    color: var(--warn);
  }
  .out.refused,
  .out.fail {
    color: var(--crit);
  }
  .note,
  .sub {
    color: var(--text-muted);
    font-size: 12px;
    margin: var(--space-1) 0;
    overflow-wrap: anywhere;
  }
  .error {
    color: var(--crit);
  }
  .adds,
  .holes {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    margin: var(--space-1) 0;
  }
  .mini {
    font: inherit;
    font-size: 12px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    padding: 0 var(--space-2);
    cursor: pointer;
  }
  .firings li {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    padding: 2px 0;
  }
  .firings li.skipped {
    color: var(--text-muted);
  }
  .firings li.unknown {
    color: var(--warn);
  }
  footer {
    border-top: 1px solid var(--border);
    padding-top: var(--space-2);
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    justify-content: flex-end;
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
  .btn.primary {
    color: var(--accent-contrast);
    background: var(--accent);
    border-color: var(--accent);
  }
  .btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
</style>

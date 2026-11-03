<script lang="ts">
  import ConfirmDialog from '../ConfirmDialog.svelte';
  import { preview, reasonKey, run, type Plan, type Result } from '../../lib/actions';
  import { ApiError } from '../../lib/api';
  import { t } from '../../lib/i18n';
  import { toast } from '../../lib/toast.svelte';

  // One action of the drawer through the actions registry: preview first,
  // the confirmation the server asks for (a destructive plan, maybe a name
  // to type), then run, with the plan's token (SECURITY S5).
  let {
    kind,
    action,
    target,
    name,
    label,
    system = false,
    onchanged,
  }: {
    kind: string;
    action: string;
    target: string;
    name: string;
    label: string;
    system?: boolean;
    onchanged?: () => void;
  } = $props();

  let pending = $state<Plan | null>(null);
  let busy = $state(false);

  function report(res: Result): void {
    const bad = [...res.blocked, ...res.failed][0];
    if (res.done === 0 && bad) {
      const reason = bad.reason === 'error' && bad.detail ? bad.detail : t(reasonKey(bad.reason), { detail: bad.detail ?? '', user: bad.detail ?? '' });
      toast(t('inspect.failed', { verb: label, name, reason }), { tone: 'error' });
      return;
    }
    toast(t('inspect.done', { verb: label, name }));
    onchanged?.();
  }

  async function execute(token?: string, typed?: string): Promise<void> {
    busy = true;
    if (system) toast(t('svc.authHint'));
    try {
      report(await run(kind, action, target, {}, token, typed || undefined));
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    } finally {
      busy = false;
    }
  }

  async function start(): Promise<void> {
    try {
      const plan = await preview(kind, action, target);
      if (plan.items.length === 0) report({ done: 0, failed: [], blocked: plan.blocked });
      else if (plan.risk === 'destructive') pending = plan;
      else await execute(plan.token);
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t('action.failed'), { tone: 'error' });
    }
  }

  async function confirmed(typed: string): Promise<void> {
    const plan = pending;
    pending = null;
    if (plan) await execute(plan.token, typed);
  }
</script>

<button type="button" disabled={busy} onclick={start}>{label}</button>

<ConfirmDialog
  plan={pending}
  title={t('inspect.confirmTitle', { verb: label, name })}
  verb={label}
  onconfirm={confirmed}
  oncancel={() => (pending = null)}
/>

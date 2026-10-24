<script lang="ts">
  // A horizontal usage bar; turns warn/crit near full.
  let { value, label, warn = 80, crit = 90 }: { value: number; label: string; warn?: number; crit?: number } = $props();
  const level = $derived(value >= crit ? 'crit' : value >= warn ? 'warn' : 'ok');
  const width = $derived(`${Math.min(100, Math.max(0, value))}%`);
</script>

<div class="meter" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(value)}>
  <div class="fill {level}" style:width></div>
</div>

<style>
  .meter {
    height: 6px;
    border-radius: 3px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
    transition: width var(--motion);
  }
  .fill.warn {
    background: var(--warn);
  }
  .fill.crit {
    background: var(--crit);
  }
</style>

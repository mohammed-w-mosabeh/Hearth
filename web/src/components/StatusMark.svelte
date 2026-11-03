<script lang="ts">
  import { statusShape, type StatusKind } from '../lib/status';

  // A shape and a colour, with the words for screen readers. label says what
  // the status is here ("Failed", "Exposed"): the shape alone names no state.
  let { kind, label }: { kind: StatusKind; label: string } = $props();
</script>

<span class="mark {kind}"><span aria-hidden="true">{statusShape[kind]}</span><span class="visually-hidden">{label}</span></span>

<style>
  .mark {
    display: inline-block;
    line-height: 1;
  }
  .ok {
    color: var(--ok);
  }
  .degraded,
  .warning {
    color: var(--warn);
  }
  .failed {
    color: var(--crit);
  }
  .stopped {
    color: var(--text-muted);
  }
</style>

<script lang="ts">
  import { sparkPath, type Pt } from '../lib/rows';

  // A row's last 10 minutes as one line (ROADMAP 8.8): plain SVG, not uPlot,
  // so a long list stays cheap. The figure beside it says the same in words.
  let { points, floor = 1 }: { points: Pt[]; floor?: number } = $props();

  const W = 64;
  const H = 16;
  const d = $derived(sparkPath(points, W, H, floor));
</script>

<svg class="spark" width={W} height={H} viewBox="0 0 {W} {H}" aria-hidden="true">
  <line x1="0" x2={W} y1={H - 0.5} y2={H - 0.5} />
  {#if d}<path {d} />{/if}
</svg>

<style>
  .spark {
    display: block;
    overflow: visible;
  }
  /* The 10-minute frame, so a line that has only started reads as recent. */
  line {
    stroke: var(--border);
    stroke-width: 1;
  }
  path {
    fill: none;
    stroke: var(--text-muted);
    stroke-width: 1.25;
    stroke-linejoin: round;
  }
</style>

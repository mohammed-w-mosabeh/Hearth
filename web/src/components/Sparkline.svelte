<script lang="ts">
  import { THEME_EVENT } from '../lib/theme';
  import type uPlot from 'uplot';
  import { WINDOW_S } from '../lib/metrics';

  // A small live line chart without axes. Colors are CSS custom properties
  // (e.g. "--accent") read at draw time, so light/dark follow the tokens.
  let {
    data,
    colors = ['--accent'],
    min = 0,
    max,
    height = 44,
    span = WINDOW_S,
    label,
  }: {
    data: [number[], ...(number | null)[][]];
    colors?: string[];
    min?: number;
    max?: number;
    height?: number;
    span?: number; // seconds shown
    label: string;
  } = $props();

  let el: HTMLDivElement;
  let chart = $state.raw<uPlot | null>(null);

  function rgba(color: string, alpha: number): string {
    const hex = /^#([0-9a-f]{6})$/i.exec(color.trim())?.[1];
    if (hex === undefined) return 'transparent';
    const n = parseInt(hex, 16);
    return `rgba(${n >> 16}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
  }

  $effect(() => {
    let disposed = false;
    let current: uPlot | null = null;
    const scheme = matchMedia('(prefers-color-scheme: dark)');

    async function build(): Promise<void> {
      const { default: UPlot } = await import('../lib/uplot');
      if (disposed) return;
      current?.destroy();
      const css = getComputedStyle(el);
      const opts: uPlot.Options = {
        width: Math.max(el.clientWidth, 1),
        height,
        legend: { show: false },
        cursor: { show: false },
        select: { show: false, left: 0, top: 0, width: 0, height: 0 },
        axes: [{ show: false }, { show: false }],
        scales: {
          x: { time: true, range: (_u, _lo, hi) => [hi - span, hi] },
          y: { range: (_u, _lo, hi) => [min, max ?? Math.max(hi * 1.1, min + 1)] },
        },
        series: [
          {},
          ...colors.map((c, i) => {
            const stroke = css.getPropertyValue(c).trim();
            const s: uPlot.Series = { stroke, width: 1.5, points: { show: false } };
            if (i === 0) s.fill = rgba(stroke, 0.15);
            return s;
          }),
        ],
      };
      current = new UPlot(opts, data as uPlot.AlignedData, el);
      chart = current;
    }

    void build();
    const rebuild = () => void build();
    scheme.addEventListener('change', rebuild);
    window.addEventListener(THEME_EVENT, rebuild);
    const ro = new ResizeObserver(() => current?.setSize({ width: Math.max(el.clientWidth, 1), height }));
    ro.observe(el);
    return () => {
      disposed = true;
      ro.disconnect();
      scheme.removeEventListener('change', rebuild);
      window.removeEventListener(THEME_EVENT, rebuild);
      current?.destroy();
      chart = null;
    };
  });

  $effect(() => {
    chart?.setData(data as uPlot.AlignedData);
  });
</script>

<div class="spark" bind:this={el} role="img" aria-label={label} style:height="{height}px"></div>

<style>
  .spark {
    width: 100%;
    overflow: hidden;
  }
</style>

<script lang="ts">
  import type uPlot from 'uplot';
  import { format, type Unit } from '../lib/charts';
  import type { Columns } from '../lib/history';
  import { t } from '../lib/i18n';

  // A full time-series chart: axes, hover cursor, legend with live values.
  // Series colors are --series-N tokens; with a single series and bucket
  // maxima available, a dashed line shows the peaks the averages hide.
  let {
    title,
    unit,
    keys,
    labels,
    cols,
    xRange,
    min,
    max,
    height = 180,
  }: {
    title: string;
    unit: Unit;
    keys: string[];
    labels: string[];
    cols: Columns;
    xRange: [number, number];
    min?: number | undefined;
    max?: number | undefined;
    height?: number;
  } = $props();

  let el: HTMLDivElement;
  let chart = $state.raw<uPlot | null>(null);
  // Read by the x scale's range function on every redraw.
  let xr: [number, number] = [0, 1];

  const peak = $derived(keys.length === 1 && cols.max !== undefined);
  const shape = $derived(`${keys.join('|')}#${peak}#${labels.join('|')}`);

  function data(): uPlot.AlignedData {
    const cols0 = cols;
    const x = cols0.t.map((ms) => ms / 1000);
    const ys = keys.map((k) => cols0.series[k] ?? x.map(() => null));
    if (peak) ys.push(cols0.max?.[keys[0] ?? ''] ?? x.map(() => null));
    return [x, ...ys] as uPlot.AlignedData;
  }

  $effect(() => {
    void shape; // rebuild when the set of series changes
    let disposed = false;
    let current: uPlot | null = null;
    const scheme = matchMedia('(prefers-color-scheme: dark)');
    const fmt = (v: number | null) => (v === null || v === undefined ? '–' : format(unit, v));

    async function build(): Promise<void> {
      const { default: UPlot } = await import('../lib/uplot');
      if (disposed) return;
      current?.destroy();
      const css = getComputedStyle(el);
      const color = (i: number) => css.getPropertyValue(`--series-${(i % 8) + 1}`).trim();
      const muted = css.getPropertyValue('--text-muted').trim();
      const grid = css.getPropertyValue('--border').trim();
      const axis = { stroke: muted, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid, width: 1 } };
      const series: uPlot.Series[] = [
        { label: t('chart.time') },
        ...keys.map((_, i) => ({ label: labels[i] ?? '', stroke: color(i), width: 1.5, value: (_u: uPlot, v: number | null) => fmt(v), points: { show: false } })),
      ];
      if (peak) {
        series.push({
          label: t('resources.peak', { name: labels[0] ?? '' }),
          stroke: color(0),
          width: 1,
          dash: [4, 4],
          value: (_u: uPlot, v: number | null) => fmt(v),
          points: { show: false },
        });
      }
      const opts: uPlot.Options = {
        width: Math.max(el.clientWidth, 1),
        height,
        series,
        cursor: { drag: { x: false, y: false } },
        scales: {
          x: { time: true, range: () => xr },
          y: {
            range: (_u, lo, hi) => {
              const a = min ?? Math.min(lo, 0);
              const b = max ?? (hi > a ? hi * 1.1 : a + 1);
              return [a, b];
            },
          },
        },
        axes: [
          { ...axis },
          { ...axis, size: 92, values: (_u, splits) => splits.map((v) => fmt(v)) },
        ],
      };
      current = new UPlot(opts, data(), el);
      chart = current;
    }

    void build();
    const rebuild = () => void build();
    scheme.addEventListener('change', rebuild);
    const ro = new ResizeObserver(() => current?.setSize({ width: Math.max(el.clientWidth, 1), height }));
    ro.observe(el);
    return () => {
      disposed = true;
      ro.disconnect();
      scheme.removeEventListener('change', rebuild);
      current?.destroy();
      chart = null;
    };
  });

  $effect(() => {
    xr = xRange;
    chart?.setData(data());
  });
</script>

<section class="chart">
  <h2>{title}</h2>
  <div bind:this={el} role="img" aria-label={title}></div>
</section>

<style>
  .chart {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
    padding: var(--space-4);
    min-width: 0;
  }
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin-bottom: var(--space-2);
  }
  .chart :global(.u-legend) {
    font-size: 12px;
    color: var(--text-muted);
    text-align: start;
    font-variant-numeric: tabular-nums;
  }
  .chart :global(.u-legend .u-value) {
    color: var(--text);
  }
  .chart :global(.u-legend .u-series) {
    padding: 0 var(--space-2) 0 0;
  }
</style>

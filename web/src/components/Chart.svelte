<script lang="ts">
  import { THEME_EVENT } from '../lib/theme';
  import type uPlot from 'uplot';
  import { format, type Unit } from '../lib/charts';
  import type { Columns } from '../lib/history';
  import { t } from '../lib/i18n';
  import { markerText, nearest, type Marker } from '../lib/markers';

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
    markers = [],
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
    markers?: Marker[];
  } = $props();

  let el: HTMLDivElement;
  let chart = $state.raw<uPlot | null>(null);
  // Read by the x scale's range function on every redraw.
  let xr: [number, number] = [0, 1];
  // Read by the draw hook; events are lines, the text shows on hover.
  let marks: Marker[] = [];
  let hovered = $state.raw<Marker | null>(null);

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
      const line = css.getPropertyValue('--text-muted').trim();
      const alarm = css.getPropertyValue('--warn').trim();
      const drawMarks = (u: uPlot) => {
        const c = u.ctx;
        c.save();
        c.beginPath();
        c.rect(u.bbox.left, u.bbox.top, u.bbox.width, u.bbox.height);
        c.clip();
        c.setLineDash([2, 4]);
        c.lineWidth = 1;
        for (const m of marks) {
          const x = u.valToPos(m.t / 1000, 'x', true);
          c.strokeStyle = m.kind === 'alert' ? alarm : line;
          c.beginPath();
          c.moveTo(x, u.bbox.top);
          c.lineTo(x, u.bbox.top + u.bbox.height);
          c.stroke();
        }
        c.restore();
      };
      const hoverMarks = (u: uPlot) => {
        const left = u.cursor.left ?? -1;
        const items = marks.map((m) => ({ m, x: u.valToPos(m.t / 1000, 'x') }));
        const near = left < 0 ? null : nearest(items, left, 6);
        if ((near?.m ?? null) !== hovered) hovered = near?.m ?? null;
      };
      const opts: uPlot.Options = {
        plugins: [{ hooks: { draw: [drawMarks], setCursor: [hoverMarks] } }],
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
    xr = xRange;
    chart?.setData(data());
  });

  $effect(() => {
    marks = markers;
    chart?.redraw(false, false);
  });
</script>

<section class="chart">
  <h2>{title}</h2>
  <div bind:this={el} role="img" aria-label={title}></div>
  {#if markers.length > 0}
    <p class="marker">{hovered ? markerText(hovered) : ' '}</p>
  {/if}
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
  .marker {
    margin: var(--space-1) 0 0;
    min-height: 1.4em;
    font-size: 12px;
    color: var(--text-muted);
    overflow-wrap: anywhere;
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

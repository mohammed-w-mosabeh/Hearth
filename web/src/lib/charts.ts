// The Resources page charts: which metrics each shows, in what unit, and
// how a metric name reads as a legend label.

import { bytes, celsius, mhz, pct, rate, rpm, watts } from './format';
import { t, type MessageKey } from './i18n';

export type Unit = 'pct' | 'bytes' | 'rate' | 'c' | 'mhz' | 'w' | 'rpm' | 'load';

export interface ChartSpec {
  id: string;
  title: MessageKey;
  sel: string[];
  unit: Unit;
  min?: number;
  max?: number;
}

export const CHARTS: ChartSpec[] = [
  { id: 'cpu', title: 'chart.cpu', sel: ['cpu.usage'], unit: 'pct', min: 0, max: 100 },
  { id: 'load', title: 'chart.load', sel: ['load1', 'load5', 'load15'], unit: 'load', min: 0 },
  { id: 'freq', title: 'chart.freq', sel: ['cpu.freq'], unit: 'mhz', min: 0 },
  { id: 'mem', title: 'chart.mem', sel: ['mem.used', 'mem.cached', 'swap.used'], unit: 'bytes', min: 0 },
  { id: 'diskio', title: 'chart.diskio', sel: ['disk.read:', 'disk.write:'], unit: 'rate', min: 0 },
  { id: 'diskused', title: 'chart.diskused', sel: ['disk.used:'], unit: 'bytes', min: 0 },
  { id: 'net', title: 'chart.net', sel: ['net.rx:', 'net.tx:'], unit: 'rate', min: 0 },
  { id: 'gpuutil', title: 'chart.gpuutil', sel: ['gpu.util:'], unit: 'pct', min: 0, max: 100 },
  { id: 'gpumem', title: 'chart.gpumem', sel: ['gpu.mem:'], unit: 'bytes', min: 0 },
  { id: 'gputemp', title: 'chart.gputemp', sel: ['gpu.temp:'], unit: 'c' },
  { id: 'gpupower', title: 'chart.gpupower', sel: ['gpu.power:'], unit: 'w', min: 0 },
  { id: 'gpufreq', title: 'chart.gpufreq', sel: ['gpu.freq:'], unit: 'mhz', min: 0 },
  { id: 'temps', title: 'chart.temps', sel: ['cpu.temp', 'temp:'], unit: 'c' },
  { id: 'fans', title: 'chart.fans', sel: ['fan:'], unit: 'rpm', min: 0 },
  { id: 'battery', title: 'chart.battery', sel: ['battery.pct:'], unit: 'pct', min: 0, max: 100 },
  { id: 'batteryrate', title: 'chart.batteryrate', sel: ['battery.rate:'], unit: 'w', min: 0 },
];

/** Per-core lines are opt-in; they join the CPU chart. */
export const CORES = 'cpu.core.';

/** Every selector the page asks the server for (<= 32). */
export function allSelectors(): string[] {
  return [...CHARTS.flatMap((c) => c.sel), CORES];
}

/** Query string asking for every chart's metrics, plus extra params. */
export function query(extra: Record<string, string>): string {
  const q = new URLSearchParams(extra);
  for (const m of allSelectors()) q.append('m', m);
  return q.toString();
}

export function format(unit: Unit, v: number): string {
  switch (unit) {
    case 'pct':
      return pct(v);
    case 'bytes':
      return bytes(v);
    case 'rate':
      return rate(v);
    case 'c':
      return celsius(v);
    case 'mhz':
      return mhz(v);
    case 'w':
      return watts(v);
    case 'rpm':
      return rpm(v);
    case 'load':
      return v.toFixed(2);
  }
}

const fixed: Record<string, MessageKey> = {
  'cpu.usage': 'series.total',
  load1: 'series.load1',
  load5: 'series.load5',
  load15: 'series.load15',
  'cpu.freq': 'series.average',
  'mem.used': 'series.used',
  'mem.cached': 'series.cached',
  'swap.used': 'series.swap',
  'cpu.temp': 'series.cpu',
};

/** Legend label of a metric; gpuNames maps PCI address to a GPU name. */
export function label(metric: string, gpuNames: Record<string, string> = {}): string {
  const key = fixed[metric];
  if (key) return t(key);
  const i = metric.indexOf(':');
  const kind = i < 0 ? metric : metric.slice(0, i);
  const dev = i < 0 ? '' : metric.slice(i + 1);
  if (metric.startsWith('cpu.core.')) return t('series.core', { n: metric.slice(9) });
  switch (kind) {
    case 'disk.read':
      return t('series.read', { dev });
    case 'disk.write':
      return t('series.write', { dev });
    case 'net.rx':
      return t('series.rx', { dev });
    case 'net.tx':
      return t('series.tx', { dev });
    case 'temp':
    case 'fan':
      return dev.replace('/', ' ');
    default:
      return kind.startsWith('gpu.') ? (gpuNames[dev] ?? dev) : dev || metric;
  }
}

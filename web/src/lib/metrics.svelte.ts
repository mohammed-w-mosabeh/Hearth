import { api } from './api';
import { liveQuery, seriesValues, totalNet, Window } from './metrics';
import type { LiveSeries, Sample } from './types';
import type { Live } from './ws';

// The Home page's live metrics: the latest sample plus the sparkline window
// (see metrics.ts). Window is deliberately not reactive; `version` is
// bumped on each change so readers re-derive once per sample.

export class LiveMetrics {
  latest = $state.raw<Sample | null>(null);
  /** Bumped on every change to the window, for $derived readers. */
  version = $state(0);
  readonly window = new Window();
  #unsub: () => void;
  #closed = false;

  constructor(live: Live) {
    // Subscribing makes the sampler switch to 1 s at once.
    this.#unsub = live.subscribe('metrics', (data) => this.#onSample(data as Sample));
    void this.#backfill();
  }

  /** Sparkline data for keys; reading it in a $derived tracks updates. */
  series(keys: string[]): [number[], ...(number | null)[][]] {
    void this.version;
    return this.window.data(keys);
  }

  close(): void {
    this.#closed = true;
    this.#unsub();
  }

  async #backfill(): Promise<void> {
    try {
      const live = await api<LiveSeries>(`/metrics/live?${liveQuery(Date.now())}`);
      if (this.#closed) return;
      this.window.prepend(totalNet(live));
      this.version++;
    } catch {
      // Sparklines just start empty.
    }
  }

  #onSample(s: Sample): void {
    if (this.#closed || typeof s !== 'object' || s === null || typeof s.t !== 'string') return;
    this.latest = s;
    this.window.push(Date.parse(s.t) / 1000, seriesValues(s));
    this.version++;
  }
}

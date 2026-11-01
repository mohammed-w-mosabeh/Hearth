// Sleep and the charge limit (ROADMAP 6.4): GET /api/v1/power/sleep.

export interface Inhibitor {
  what: string;
  who: string;
  why: string;
  mode: string;
  uid: number;
  pid: number;
}

export interface SleepStretch {
  from: number;
  to?: number;
  mode: string;
}

export interface ChargeLimit {
  battery: string;
  via: 'kernel' | 'upower' | string;
  end?: number;
  enabled: boolean;
}

export interface SleepReport {
  inhibitors: Inhibitor[];
  sleeps: SleepStretch[];
  limits: ChargeLimit[];
  limited?: boolean;
}

/** The usual limits, plus the current one if it is another. */
export function limitChoices(current?: number): number[] {
  const usual = [60, 70, 80, 90, 100];
  if (current === undefined || usual.includes(current)) return usual;
  return [...usual, current].sort((a, b) => a - b);
}

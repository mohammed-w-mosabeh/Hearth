// Shapes of the hearthd JSON this UI reads (internal/collect, internal/power).

export interface CPU {
  usage: number;
  cores: number[];
  freq_mhz: number[];
  load1: number;
  load5: number;
  load15: number;
}

export interface Mem {
  total: number;
  available: number;
  used: number;
  cached: number;
  swap_total: number;
  swap_used: number;
}

export interface Disk {
  mount: string;
  device: string;
  fstype: string;
  total: number;
  used: number;
  avail: number;
  pct: number;
}

export interface DiskIO {
  name: string;
  read_bps: number;
  write_bps: number;
  busy_pct: number;
}

export interface NetIf {
  name: string;
  rx_bps: number;
  tx_bps: number;
  virtual: boolean;
}

export interface Temp {
  chip: string;
  label: string;
  c: number;
  max_c?: number;
  crit_c?: number;
}

export interface Fan {
  chip: string;
  label: string;
  rpm: number;
}

export interface GPU {
  card: string;
  pci: string;
  vendor: string;
  driver: string;
  name: string;
  sleeping: boolean;
  util_pct?: number;
  mem_used?: number;
  mem_total?: number;
  temp_c?: number;
  power_w?: number;
  freq_mhz?: number;
}

export interface Battery {
  id: string;
  model?: string;
  vendor?: string;
  pct: number;
  state: string;
  rate_w: number;
  energy_wh: number;
  full_wh: number;
  design_wh: number;
  health_pct: number;
  cycles?: number;
  to_empty_s?: number;
  to_full_s?: number;
}

export interface Power {
  batteries: Battery[] | null;
  on_ac?: boolean;
  profile?: { active: string; available: string[] | null; degraded?: string };
}

export interface Sample {
  t: string;
  cpu: CPU;
  mem: Mem;
  disks: Disk[] | null;
  disk_io: DiskIO[] | null;
  net: NetIf[] | null;
  temps: Temp[] | null;
  fans: Fan[] | null;
  gpus: GPU[] | null;
  power: Power;
  cpu_temp_c?: number;
  partial?: boolean;
}

/** GET /api/v1/metrics/live */
export interface LiveSeries {
  t: number[];
  series: Record<string, (number | null)[]>;
}

export type GroupKind = 'app' | 'service' | 'user-service' | 'container' | 'session' | 'kernel';

export interface Proc {
  pid: number;
  start: number;
  name: string;
  state: string;
  cpu: number;
  rss: number;
}

export interface ProcGroup {
  id: string;
  kind: GroupKind;
  name: string;
  unit?: string;
  user: string;
  count: number;
  cpu: number;
  mem: number;
  read_bps?: number;
  write_bps?: number;
  io_partial?: boolean;
  procs: Proc[];
}

/** WS topic "procs" */
export interface ProcSnapshot {
  t: string;
  groups: ProcGroup[];
  partial?: boolean;
}

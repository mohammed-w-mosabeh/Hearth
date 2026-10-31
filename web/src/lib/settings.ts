// Settings page: shapes of /api/v1/system/* (internal/settings) and helpers.

export interface Hostname {
  static: string;
  pretty: string;
  transient: string;
  chassis?: string;
}

export interface TimeState {
  timezone: string;
  ntp: boolean;
  can_ntp: boolean;
  ntp_synchronized: boolean;
  local_rtc: boolean;
}

export interface PowerActions {
  poweroff: boolean;
  reboot: boolean;
  suspend: boolean;
  hibernate: boolean;
}

export interface Backlight {
  name: string;
  value: number;
  max: number;
  percent: number;
}

export interface SystemInfo {
  hostname: Hostname | null;
  time: TimeState | null;
  power: PowerActions | null;
  backlights: Backlight[];
}

export interface NetDevice {
  name: string;
  type: 'wifi' | 'ethernet' | 'other';
  state: string;
  connection?: string;
}

export interface AccessPoint {
  ssid: string;
  device: string;
  strength: number;
  security: 'open' | 'psk' | 'enterprise';
  active: boolean;
  saved: boolean;
}

export interface Connection {
  uuid: string;
  name: string;
  type: string;
  ssid?: string;
  active: boolean;
  autoconnect: boolean;
}

export interface NetworkState {
  available: boolean;
  wifi_enabled: boolean;
  wifi_hardware: boolean;
  devices: NetDevice[];
  access_points: AccessPoint[];
  connections: Connection[];
}

export interface AudioDevice {
  id: number;
  name: string;
  kind: 'output' | 'input';
  volume: number;
  muted: boolean;
  default: boolean;
}

export interface AudioState {
  available: boolean;
  devices: AudioDevice[];
}

export interface AutostartEntry {
  id: string;
  name: string;
  comment?: string;
  enabled: boolean;
  origin: 'system' | 'user';
}

/** Signal strength as 1 to 4 bars. */
export function bars(strength: number): number {
  return strength >= 75 ? 4 : strength >= 50 ? 3 : strength >= 25 ? 2 : 1;
}

/** The saved Wi-Fi connection for a network name, if any. */
export function savedFor(conns: Connection[], ssid: string): Connection | undefined {
  return conns.find((c) => c.type === 'wifi' && c.ssid === ssid);
}

/** Connections that are not Wi-Fi networks (wired, VPN...): listed on their own. */
export function otherConnections(conns: Connection[]): Connection[] {
  return conns.filter((c) => c.type !== 'wifi');
}

/** Slider percent (0..150) as wpctl's volume parameter ("0.75"). */
export function volumeParam(percent: number): string {
  const p = Math.min(150, Math.max(0, Math.round(percent)));
  return (p / 100).toFixed(2);
}

/** A hostname as hostnamed stores it: lower case, valid labels, 64 bytes at most. */
export function validHostname(s: string): boolean {
  return s.length > 0 && s.length <= 64 && s.split('.').every((l) => /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(l));
}

/** Zones grouped by region for a <select> with <optgroup>s. */
export function zoneGroups(zones: string[]): { region: string; zones: string[] }[] {
  const groups = new Map<string, string[]>();
  for (const z of zones) {
    const region = z.includes('/') ? (z.split('/')[0] ?? '') : '';
    groups.set(region, [...(groups.get(region) ?? []), z]);
  }
  return [...groups.entries()].map(([region, zs]) => ({ region, zones: zs }));
}

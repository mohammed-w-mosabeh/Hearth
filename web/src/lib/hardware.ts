// Hardware (ROADMAP 6.7): GET /api/v1/hardware and /hardware/firmware.

export interface Device {
  id: string;
  vendor?: string;
  name?: string;
  class?: string;
}

export interface HwDisk {
  name: string;
  model?: string;
  bytes: number;
}

export interface Inventory {
  cpu?: string;
  threads: number;
  mem_bytes: number;
  vendor?: string;
  product?: string;
  board?: string;
  bios?: string;
  bios_date?: string;
  pci: Device[];
  usb: Device[];
  disks: HwDisk[];
}

export interface Firmware {
  id: string;
  name: string;
  vendor?: string;
  version?: string;
  update?: string;
}

export interface FirmwareReport {
  available: boolean;
  devices: Firmware[];
}

/** What to call a device: its name, else its vendor, else its ID. */
export function deviceName(d: Device): string {
  return d.name || d.vendor || d.id;
}

export function updatable(devices: readonly Firmware[]): number {
  return devices.filter((d) => d.update && d.update !== d.version).length;
}

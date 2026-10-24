import { clearKey } from './session';

export class Unauthorized extends Error {}

let getKey: () => string | null = () => null;
let onUnauthorized: () => void = () => {};

export function setSession(key: () => string | null, unauthorized: () => void): void {
  getKey = key;
  onUnauthorized = unauthorized;
}

/** fetch for /api/v1 with the bearer key (SECURITY S2). */
export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  const key = getKey();
  if (key) headers.set('Authorization', `Bearer ${key}`);
  if (init.body !== undefined) headers.set('Content-Type', 'application/json');
  const res = await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'omit' });
  if (res.status === 401) {
    clearKey({ storage: localStorage });
    onUnauthorized();
    throw new Unauthorized();
  }
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return (await res.json()) as T;
}

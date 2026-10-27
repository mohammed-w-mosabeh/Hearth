import { clearKey } from './session';

export class Unauthorized extends Error {}

/** A non-2xx response; message is the server's {"error": ...} if any. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

let getKey: () => string | null = () => null;
let onUnauthorized: () => void = () => {};

export function setSession(key: () => string | null, unauthorized: () => void): void {
  getKey = key;
  onUnauthorized = unauthorized;
}

/** The session key, for WebSockets other than Live (they send it as a subprotocol). */
export function sessionKey(): string | null {
  return getKey();
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
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error?: unknown };
      if (typeof body.error === 'string') msg = body.error;
    } catch {
      // not JSON (e.g. a plain-text http.Error)
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

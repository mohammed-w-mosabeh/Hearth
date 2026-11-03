// One WebSocket to /api/v1/ws with topic subscriptions. The session key
// travels as a subprotocol because browsers cannot set Authorization on a
// WebSocket (SECURITY S2); the server echoes only "hearth.v1".

export type WsState = 'connecting' | 'connected' | 'offline';

type Handler = (data: unknown) => void;

export class Live {
  #key: string;
  #ws: WebSocket | null = null;
  #handlers = new Map<string, Set<Handler>>();
  #listeners = new Map<string, Set<Handler>>();
  #retry = 0;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #closed = false;
  #onState: (s: WsState) => void;

  constructor(key: string, onState: (s: WsState) => void) {
    this.#key = key;
    this.#onState = onState;
    this.#connect();
  }

  /** Hears a topic someone else subscribed to, without subscribing: the
   *  sampler runs at 1 s only for subscribers. */
  listen(topic: string, fn: Handler): () => void {
    let set = this.#listeners.get(topic);
    if (!set) {
      set = new Set();
      this.#listeners.set(topic, set);
    }
    set.add(fn);
    return () => set.delete(fn);
  }

  subscribe(topic: string, fn: Handler): () => void {
    let set = this.#handlers.get(topic);
    if (!set) {
      set = new Set();
      this.#handlers.set(topic, set);
      this.#send({ op: 'sub', topic });
    }
    set.add(fn);
    return () => {
      set.delete(fn);
      if (set.size === 0) {
        this.#handlers.delete(topic);
        this.#send({ op: 'unsub', topic });
      }
    };
  }

  close(): void {
    this.#closed = true;
    clearTimeout(this.#timer);
    this.#ws?.close();
  }

  #connect(): void {
    this.#onState('connecting');
    const url = new URL('/api/v1/ws', location.href);
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(url, ['hearth.v1', `hearth.key.${this.#key}`]);
    this.#ws = ws;
    ws.onopen = () => {
      this.#retry = 0;
      this.#onState('connected');
      for (const topic of this.#handlers.keys()) this.#send({ op: 'sub', topic });
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data !== 'string') return;
      let msg: { topic?: unknown; data?: unknown };
      try {
        msg = JSON.parse(ev.data) as typeof msg;
      } catch {
        return;
      }
      if (typeof msg.topic !== 'string') return;
      for (const fn of this.#handlers.get(msg.topic) ?? []) fn(msg.data);
      for (const fn of this.#listeners.get(msg.topic) ?? []) fn(msg.data);
    };
    ws.onclose = () => {
      this.#ws = null;
      this.#onState('offline');
      if (this.#closed) return;
      const delay = Math.min(30_000, 500 * 2 ** this.#retry++);
      this.#timer = setTimeout(() => this.#connect(), delay);
    };
  }

  #send(msg: { op: 'sub' | 'unsub'; topic: string }): void {
    if (this.#ws?.readyState === WebSocket.OPEN) this.#ws.send(JSON.stringify(msg));
  }
}

// Connects a page's Yjs document to the wiki backend (internal/live).
//
// The protocol is the wiki's own, not y-websocket: the server relays and
// stores updates without decoding them (docs/adr/0001-yjs-relay-in-go.md).
// Binary frames start with a type byte: 1 update, 2 batch (on join),
// 3 awareness, 5 snapshot (to the server). Text frames are JSON control
// messages.
import * as Y from 'yjs';
import {
  Awareness,
  applyAwarenessUpdate,
  encodeAwarenessUpdate,
  removeAwarenessStates,
} from 'y-protocols/awareness';
import { ApiError, apiURL } from '@/api/client';
import { atLeast, getLiveTicket, type Level } from '@/api/wiki';

const FRAME_UPDATE = 1;
const FRAME_BATCH = 2;
const FRAME_AWARENESS = 3;
const FRAME_SNAPSHOT = 5;

/** The server needs a fresh ticket at least every 3 minutes. */
const REFRESH_EVERY = 60_000;

/** Must match the backend's close codes (internal/live/conn.go). */
const CLOSE_EXPIRED = 4401;
const CLOSE_NO_ACCESS = 4404;
const CLOSE_RESTORED = 4409;

/**
 * 'restored' means a revision was made current on the server; this
 * provider's document belongs to the replaced timeline, so the page must
 * build a fresh provider (useLivePage does).
 */
export type LiveStatus = 'connecting' | 'synced' | 'offline' | 'gone' | 'restored';

export type LiveState = {
  status: LiveStatus;
  level: Level;
  /** True once the stored document has loaded; the editor shows from then on. */
  loaded: boolean;
  /** Last message for the person (refused edit, page gone, storage off). */
  message: string | null;
};

/** Search text and linked page ids, sent with each snapshot. */
export type ContentSummary = { text: string; links: string[] };

const REMOTE = 'remote';

export class WikiProvider {
  readonly doc = new Y.Doc();
  readonly awareness = new Awareness(this.doc);
  private state: LiveState = { status: 'connecting', level: 'none', loaded: false, message: null };
  private listeners = new Set<() => void>();
  private ws: WebSocket | null = null;
  private joined = false;
  private received: Uint8Array[] = [];
  private attempts = 0;
  private retryTimer: number | undefined;
  private refreshTimer: number | undefined;
  private destroyed = false;
  private summarize: () => ContentSummary = () => ({ text: '', links: [] });

  constructor(readonly pageId: string) {
    this.doc.on('update', this.onDocUpdate);
    this.awareness.on('update', this.onAwarenessUpdate);
    window.addEventListener('beforeunload', this.leave);
    void this.connect();
  }

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  };

  getState = () => this.state;

  /** The editor provides the text the server indexes for search. */
  setSummary(fn: () => ContentSummary) {
    this.summarize = fn;
  }

  destroy() {
    this.destroyed = true;
    window.clearTimeout(this.retryTimer);
    window.clearInterval(this.refreshTimer);
    window.removeEventListener('beforeunload', this.leave);
    this.leave();
    this.ws?.close(1000);
    this.ws = null;
    this.doc.off('update', this.onDocUpdate);
    this.awareness.off('update', this.onAwarenessUpdate);
    this.awareness.destroy();
    this.doc.destroy();
  }

  private set(patch: Partial<LiveState>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
  }

  private leave = () => {
    removeAwarenessStates(this.awareness, [this.doc.clientID], 'local');
  };

  private canEdit() {
    return atLeast(this.state.level, 'edit');
  }

  private send(type: number, payload: Uint8Array) {
    if (!this.joined || this.ws?.readyState !== WebSocket.OPEN) return;
    const frame = new Uint8Array(payload.length + 1);
    frame[0] = type;
    frame.set(payload, 1);
    this.ws.send(frame);
  }

  private onDocUpdate = (update: Uint8Array, origin: unknown) => {
    // Changes made while offline are sent as one diff after the next join.
    if (origin !== this && this.canEdit()) this.send(FRAME_UPDATE, update);
  };

  private onAwarenessUpdate = (
    { added, updated, removed }: { added: number[]; updated: number[]; removed: number[] },
    origin: unknown
  ) => {
    if (origin === REMOTE) return;
    this.send(FRAME_AWARENESS, encodeAwarenessUpdate(this.awareness, added.concat(updated, removed)));
  };

  private async connect() {
    if (this.destroyed) return;
    let ticket: string;
    try {
      const r = await getLiveTicket(this.pageId);
      ticket = r.ticket;
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        this.set({ status: 'gone', message: 'This page was deleted, or you no longer have access to it.' });
        return;
      }
      this.set({
        status: this.state.loaded ? 'offline' : 'connecting',
        message: e instanceof ApiError && e.status === 503 ? e.message : null,
      });
      this.retry();
      return;
    }
    if (this.destroyed) return;

    const url = apiURL(`/api/v1/pages/${this.pageId}/live`);
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    url.searchParams.set('ticket', ticket);
    const ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    this.ws = ws;
    this.joined = false;
    this.received = [];
    ws.onmessage = (ev) => {
      if (this.ws === ws) this.onMessage(ev);
    };
    ws.onclose = (ev) => {
      if (this.ws === ws) this.onClose(ev);
    };
  }

  private onMessage(ev: MessageEvent) {
    if (typeof ev.data === 'string') {
      this.onControl(JSON.parse(ev.data) as Record<string, unknown>);
      return;
    }
    const data = new Uint8Array(ev.data as ArrayBuffer);
    switch (data[0]) {
      case FRAME_BATCH: {
        const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
        for (let i = 1; i + 4 <= data.length; ) {
          const n = view.getUint32(i);
          this.received.push(data.subarray(i + 4, i + 4 + n));
          i += 4 + n;
        }
        if (this.received.length > 0) Y.applyUpdate(this.doc, Y.mergeUpdates(this.received), this);
        break;
      }
      case FRAME_UPDATE:
        Y.applyUpdate(this.doc, data.subarray(1), this);
        break;
      case FRAME_AWARENESS:
        applyAwarenessUpdate(this.awareness, data.subarray(1), REMOTE);
        break;
    }
  }

  private onControl(msg: Record<string, unknown>) {
    switch (msg.type) {
      case 'synced': {
        this.joined = true;
        this.attempts = 0;
        this.set({ status: 'synced', level: msg.level as Level, loaded: true, message: null });
        // Send what this browser has that the server does not (edits made
        // while disconnected).
        if (this.canEdit()) {
          const serverState =
            this.received.length > 0
              ? Y.encodeStateVectorFromUpdate(Y.mergeUpdates(this.received))
              : new Uint8Array([0]);
          const diff = Y.encodeStateAsUpdate(this.doc, serverState);
          if (diff.length > 2) this.send(FRAME_UPDATE, diff);
        }
        this.received = [];
        if (this.awareness.getLocalState() !== null) {
          this.send(FRAME_AWARENESS, encodeAwarenessUpdate(this.awareness, [this.doc.clientID]));
        }
        window.clearInterval(this.refreshTimer);
        this.refreshTimer = window.setInterval(() => void this.refresh(), REFRESH_EVERY);
        break;
      }
      case 'access':
        this.set({ level: msg.level as Level });
        break;
      case 'compact':
        if (this.canEdit()) this.sendSnapshot(Number(msg.upto));
        break;
      case 'error':
        this.set({ message: String(msg.message ?? '') });
        break;
    }
  }

  private sendSnapshot(upto: number) {
    let summary: ContentSummary;
    try {
      summary = this.summarize();
    } catch {
      summary = { text: '', links: [] };
    }
    const header = new TextEncoder().encode(JSON.stringify({ upto, ...summary }));
    const state = Y.encodeStateAsUpdate(this.doc);
    const body = new Uint8Array(4 + header.length + state.length);
    new DataView(body.buffer).setUint32(0, header.length);
    body.set(header, 4);
    body.set(state, 4 + header.length);
    this.send(FRAME_SNAPSHOT, body);
  }

  private async refresh() {
    try {
      const { ticket } = await getLiveTicket(this.pageId);
      if (this.joined) this.ws?.send(JSON.stringify({ type: 'refresh', ticket }));
    } catch {
      // A lost page closes the socket from the server side; anything else
      // is retried on the next interval.
    }
  }

  private onClose(ev: CloseEvent) {
    this.ws = null;
    this.joined = false;
    window.clearInterval(this.refreshTimer);
    const others = Array.from(this.awareness.getStates().keys()).filter((id) => id !== this.doc.clientID);
    removeAwarenessStates(this.awareness, others, REMOTE);
    if (this.destroyed) return;
    if (ev.code === CLOSE_NO_ACCESS) {
      this.set({ status: 'gone', message: 'This page was deleted, or you no longer have access to it.' });
      return;
    }
    if (ev.code === CLOSE_RESTORED) {
      this.set({ status: 'restored', loaded: false, message: null });
      return;
    }
    this.set({ status: this.state.loaded ? 'offline' : 'connecting' });
    if (ev.code === CLOSE_EXPIRED) {
      void this.connect();
      return;
    }
    this.retry();
  }

  private retry() {
    const delay = Math.min(30_000, 500 * 2 ** this.attempts) * (0.75 + Math.random() * 0.5);
    this.attempts++;
    window.clearTimeout(this.retryTimer);
    this.retryTimer = window.setTimeout(() => void this.connect(), delay);
  }
}

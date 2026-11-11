// WikiProvider against a fake websocket: the join batch, update framing,
// view-only connections never sending edits, snapshots on "compact", and
// what each close code does (docs/adr/0001-yjs-relay-in-go.md).
import * as Y from 'yjs';
import { afterEach, beforeEach, vi } from 'vitest';
import { ApiError } from '@/api/client';
import { WikiProvider } from '@/live/WikiProvider';

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>();
  return { ...actual, apiURL: (path: string) => new URL(path, 'http://wiki.test') };
});
vi.mock('@/api/wiki', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/wiki')>();
  return { ...actual, getLiveTicket: vi.fn() };
});

const api = await import('@/api/wiki');

class FakeSocket {
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  readyState = FakeSocket.OPEN;
  binaryType = '';
  sent: (Uint8Array | string)[] = [];
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  constructor(readonly url: URL) {
    FakeSocket.instances.push(this);
  }
  send(data: Uint8Array | string) {
    this.sent.push(data);
  }
  close() {}
  control(msg: object) {
    this.onmessage?.({ data: JSON.stringify(msg) } as MessageEvent);
  }
  binary(type: number, payload: Uint8Array) {
    const frame = new Uint8Array(payload.length + 1);
    frame[0] = type;
    frame.set(payload, 1);
    this.onmessage?.({ data: frame.buffer } as MessageEvent);
  }
  batch(updates: Uint8Array[]) {
    const size = updates.reduce((n, u) => n + 4 + u.length, 0);
    const payload = new Uint8Array(size);
    const view = new DataView(payload.buffer);
    let at = 0;
    for (const u of updates) {
      view.setUint32(at, u.length);
      payload.set(u, at + 4);
      at += 4 + u.length;
    }
    this.binary(2, payload);
  }
  shut(code: number) {
    this.onclose?.({ code } as CloseEvent);
  }
  frames(type: number) {
    return this.sent.filter((d): d is Uint8Array => typeof d !== 'string' && d[0] === type).map((d) => d.subarray(1));
  }
}

/** An update that sets the shared text to `text`. */
function textUpdate(text: string) {
  const doc = new Y.Doc();
  doc.getText('t').insert(0, text);
  return Y.encodeStateAsUpdate(doc);
}

let providers: WikiProvider[] = [];

async function connected(level: 'view' | 'edit') {
  vi.mocked(api.getLiveTicket).mockResolvedValue({ ticket: 'tk', level });
  const p = new WikiProvider('page-1');
  providers.push(p);
  await vi.waitFor(() => expect(FakeSocket.instances.length).toBeGreaterThan(0));
  const ws = FakeSocket.instances.at(-1)!;
  return { p, ws };
}

beforeEach(() => {
  FakeSocket.instances = [];
  vi.stubGlobal('WebSocket', FakeSocket);
  vi.mocked(api.getLiveTicket).mockReset();
});

afterEach(() => {
  providers.forEach((p) => p.destroy());
  providers = [];
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

test('opens the websocket with the ticket and applies the join batch', async () => {
  const { p, ws } = await connected('edit');
  expect(ws.url.protocol).toBe('ws:');
  expect(ws.url.pathname).toBe('/api/v1/pages/page-1/live');
  expect(ws.url.searchParams.get('ticket')).toBe('tk');

  ws.batch([textUpdate('hello')]);
  ws.control({ type: 'synced', level: 'edit' });
  expect(p.doc.getText('t').toString()).toBe('hello');
  expect(p.getState()).toMatchObject({ status: 'synced', level: 'edit', loaded: true });
});

test('an editor sends local edits as update frames', async () => {
  const { p, ws } = await connected('edit');
  ws.control({ type: 'synced', level: 'edit' });
  p.doc.getText('t').insert(0, 'typed');
  const updates = ws.frames(1);
  expect(updates.length).toBeGreaterThan(0);
  const other = new Y.Doc();
  updates.forEach((u) => Y.applyUpdate(other, u));
  expect(other.getText('t').toString()).toBe('typed');
});

test('edits made before joining are sent as one diff after "synced"', async () => {
  const { p, ws } = await connected('edit');
  p.doc.getText('t').insert(0, 'offline');
  expect(ws.frames(1)).toHaveLength(0);
  ws.control({ type: 'synced', level: 'edit' });
  const other = new Y.Doc();
  ws.frames(1).forEach((u) => Y.applyUpdate(other, u));
  expect(other.getText('t').toString()).toBe('offline');
});

test('a view-only connection never sends document updates', async () => {
  const { p, ws } = await connected('view');
  ws.control({ type: 'synced', level: 'view' });
  p.doc.getText('t').insert(0, 'nope');
  ws.control({ type: 'compact', upto: 3 });
  expect(ws.frames(1)).toHaveLength(0);
  expect(ws.frames(5)).toHaveLength(0);
});

test('remote updates are applied and not echoed back', async () => {
  const { p, ws } = await connected('edit');
  ws.control({ type: 'synced', level: 'edit' });
  ws.binary(1, textUpdate('remote'));
  expect(p.doc.getText('t').toString()).toBe('remote');
  expect(ws.frames(1)).toHaveLength(0);
});

test('"compact" sends a snapshot with the summary header', async () => {
  const { p, ws } = await connected('edit');
  ws.control({ type: 'synced', level: 'edit' });
  p.setSummary(() => ({ text: 'Body text', links: ['p-2'] }));
  ws.control({ type: 'compact', upto: 7 });
  const [snap] = ws.frames(5);
  const view = new DataView(snap.buffer, snap.byteOffset, snap.byteLength);
  const n = view.getUint32(0);
  const header = JSON.parse(new TextDecoder().decode(snap.subarray(4, 4 + n)));
  expect(header).toEqual({ upto: 7, text: 'Body text', links: ['p-2'] });
  const doc = new Y.Doc();
  Y.applyUpdate(doc, snap.subarray(4 + n));
});

test('close codes: no access is final, restored asks for a new provider, expired reconnects at once', async () => {
  const { p, ws } = await connected('edit');
  ws.control({ type: 'synced', level: 'edit' });
  ws.shut(4401);
  await vi.waitFor(() => expect(FakeSocket.instances).toHaveLength(2));

  FakeSocket.instances[1].control({ type: 'synced', level: 'edit' });
  FakeSocket.instances[1].shut(4409);
  expect(p.getState().status).toBe('restored');

  const second = await connected('edit');
  second.ws.shut(4404);
  expect(second.p.getState().status).toBe('gone');
  await new Promise((r) => setTimeout(r, 50));
  expect(FakeSocket.instances).toHaveLength(3);
});

test('a dropped connection retries with a delay', async () => {
  vi.useFakeTimers();
  const { ws } = await connected('edit');
  ws.control({ type: 'synced', level: 'edit' });
  ws.shut(1006);
  expect(FakeSocket.instances).toHaveLength(1);
  await vi.advanceTimersByTimeAsync(1_000);
  expect(FakeSocket.instances).toHaveLength(2);
});

test('a deleted page (ticket 404) is gone without a socket', async () => {
  vi.mocked(api.getLiveTicket).mockRejectedValue(
    new ApiError(404, { error: { code: 'not_found', message: 'page not found' } })
  );
  const p = new WikiProvider('page-1');
  providers.push(p);
  await vi.waitFor(() => expect(p.getState().status).toBe('gone'));
  expect(FakeSocket.instances).toHaveLength(0);
});

// WikiThreadStore: only anchored threads reach BlockNote, new threads are
// created anchored, and only editors may start one.
import { afterEach, beforeEach, vi } from 'vitest';
import type { RawThread } from '@/api/wiki';
import { WikiThreadStore, WikiThreadStoreAuth } from '@/live/WikiThreadStore';

vi.mock('@/api/wiki', () => ({
  listThreads: vi.fn(),
  createThread: vi.fn(),
  addThreadComment: vi.fn(),
  updateThreadComment: vi.fn(),
  deleteThreadComment: vi.fn(),
  deleteThread: vi.fn(),
  setThreadResolved: vi.fn(),
  setCommentReaction: vi.fn(),
}));

const api = await import('@/api/wiki');

function raw(id: string, anchored: boolean): RawThread {
  const now = '2026-11-11T10:00:00Z';
  return {
    id,
    page_id: 'page-1',
    anchored,
    resolved: false,
    resolved_by: null,
    resolved_at: null,
    metadata: null,
    created_at: now,
    updated_at: now,
    comments: [
      { id: `${id}-c`, user_id: 'u-1', body: [], metadata: null, reactions: [], created_at: now, updated_at: now, deleted_at: null },
    ],
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  vi.mocked(api.listThreads).mockResolvedValue([raw('inline', true), raw('page-level', false)]);
});

afterEach(() => {
  vi.useRealTimers();
});

test('only anchored threads are handed to the editor', async () => {
  const store = new WikiThreadStore('page-1', 'u-1', 'editor');
  const seen = vi.fn();
  const unsubscribe = store.subscribe(seen);
  await vi.waitFor(() => expect(seen).toHaveBeenCalled());
  const threads = seen.mock.calls.at(-1)![0] as Map<string, unknown>;
  expect([...threads.keys()]).toEqual(['inline']);
  expect(() => store.getThread('page-level')).toThrow();
  unsubscribe();
});

test('new threads from the editor are anchored', async () => {
  vi.mocked(api.createThread).mockResolvedValue(raw('new', true));
  const store = new WikiThreadStore('page-1', 'u-1', 'editor');
  const t = await store.createThread({ initialComment: { body: [] } });
  expect(api.createThread).toHaveBeenCalledWith('page-1', [], undefined, true);
  expect(t.id).toBe('new');
  expect(store.getThreads().has('new')).toBe(true);
});

test('polling stops when the last listener leaves', async () => {
  const store = new WikiThreadStore('page-1', 'u-1', 'comment');
  const unsubscribe = store.subscribe(() => undefined);
  await vi.waitFor(() => expect(api.listThreads).toHaveBeenCalledTimes(1));
  vi.advanceTimersByTime(5_000);
  await vi.waitFor(() => expect(api.listThreads).toHaveBeenCalledTimes(2));
  unsubscribe();
  vi.advanceTimersByTime(20_000);
  expect(api.listThreads).toHaveBeenCalledTimes(2);
});

test('only editors may start inline threads; comment level still replies', () => {
  const thread = { type: 'thread', id: 't', comments: [], resolved: false, createdAt: new Date(), updatedAt: new Date(), metadata: null } as never;
  const editor = new WikiThreadStoreAuth('u-1', true);
  const commenter = new WikiThreadStoreAuth('u-2', false);
  expect(editor.canCreateThread()).toBe(true);
  expect(commenter.canCreateThread()).toBe(false);
  expect(commenter.canAddComment(thread)).toBe(true);
  expect(commenter.canResolveThread(thread)).toBe(true);
  expect(commenter.canDeleteThread(thread)).toBe(false);
});

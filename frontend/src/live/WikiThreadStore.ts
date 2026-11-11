// BlockNote ThreadStore backed by the wiki's REST comments API. The server
// enforces the real rules (comment level replies, authors edit their own,
// editors delete); WikiThreadStoreAuth only mirrors them for the UI.
// Thread anchors live as marks in the Yjs document, so they sync live; the
// thread contents are fetched here and refreshed by polling.
//
// This store only holds anchored (inline) threads. Writing the anchor
// changes the document, which the server accepts from editors only, so
// comment-level people cannot start inline threads here; they start
// page-level discussions in PageDiscussions instead.
import {
  DefaultThreadStoreAuth,
  ThreadStore,
  type CommentBody,
  type CommentData,
  type ThreadData,
} from '@blocknote/core/comments';
import {
  addThreadComment,
  createThread,
  deleteThread,
  deleteThreadComment,
  listThreads,
  setCommentReaction,
  setThreadResolved,
  updateThreadComment,
  type RawComment,
  type RawThread,
} from '@/api/wiki';

const POLL_EVERY = 5_000;

function toCommentData(c: RawComment): CommentData {
  const base = {
    type: 'comment' as const,
    id: c.id,
    userId: c.user_id ?? 'unknown',
    createdAt: new Date(c.created_at),
    updatedAt: new Date(c.updated_at),
    reactions: c.reactions.map((r) => ({
      emoji: r.emoji,
      createdAt: new Date(r.created_at),
      userIds: r.user_ids,
    })),
    metadata: c.metadata,
  };
  if (c.deleted_at) {
    return { ...base, deletedAt: new Date(c.deleted_at), body: undefined };
  }
  return { ...base, body: c.body };
}

function toThreadData(t: RawThread): ThreadData {
  return {
    type: 'thread',
    id: t.id,
    createdAt: new Date(t.created_at),
    updatedAt: new Date(t.updated_at),
    comments: t.comments.map(toCommentData),
    resolved: t.resolved,
    resolvedUpdatedAt: t.resolved_at ? new Date(t.resolved_at) : undefined,
    resolvedBy: t.resolved_by ?? undefined,
    metadata: t.metadata,
  };
}

/** DefaultThreadStoreAuth, except that only editors start (anchored) threads. */
export class WikiThreadStoreAuth extends DefaultThreadStoreAuth {
  constructor(
    userId: string,
    private readonly editor: boolean
  ) {
    super(userId, editor ? 'editor' : 'comment');
  }

  canCreateThread() {
    return this.editor;
  }
}

export class WikiThreadStore extends ThreadStore {
  private threads = new Map<string, ThreadData>();
  private listeners = new Set<(threads: Map<string, ThreadData>) => void>();
  private timer: number | undefined;

  constructor(
    private readonly pageId: string,
    userId: string,
    role: 'editor' | 'comment'
  ) {
    super(new WikiThreadStoreAuth(userId, role === 'editor'));
  }

  // Marks are created by the default TipTap behaviour; no override needed.
  addThreadToDocument = undefined;

  private emit() {
    this.listeners.forEach((l) => l(this.threads));
  }

  private apply(t: RawThread) {
    if (!t.anchored) return;
    this.threads = new Map(this.threads);
    this.threads.set(t.id, toThreadData(t));
    this.emit();
  }

  private refresh = async () => {
    try {
      const raw = await listThreads(this.pageId);
      this.threads = new Map(raw.filter((t) => t.anchored).map((t) => [t.id, toThreadData(t)]));
      this.emit();
    } catch {
      // Offline or access just changed; the next poll retries.
    }
  };

  subscribe(cb: (threads: Map<string, ThreadData>) => void) {
    this.listeners.add(cb);
    if (this.listeners.size === 1) {
      void this.refresh();
      this.timer = window.setInterval(() => void this.refresh(), POLL_EVERY);
    }
    return () => {
      this.listeners.delete(cb);
      if (this.listeners.size === 0) window.clearInterval(this.timer);
    };
  }

  async createThread(options: { initialComment: { body: CommentBody; metadata?: unknown }; metadata?: unknown }) {
    const t = await createThread(this.pageId, options.initialComment.body, options.metadata, true);
    this.apply(t);
    return toThreadData(t);
  }

  async addComment(options: { comment: { body: CommentBody; metadata?: unknown }; threadId: string }) {
    const t = await addThreadComment(options.threadId, options.comment.body);
    this.apply(t);
    const td = toThreadData(t);
    return td.comments[td.comments.length - 1];
  }

  async updateComment(options: { comment: { body: CommentBody; metadata?: unknown }; threadId: string; commentId: string }) {
    this.apply(await updateThreadComment(options.commentId, options.comment.body));
  }

  async deleteComment(options: { threadId: string; commentId: string }) {
    this.apply(await deleteThreadComment(options.commentId));
  }

  async deleteThread(options: { threadId: string }) {
    await deleteThread(options.threadId);
    this.threads = new Map(this.threads);
    this.threads.delete(options.threadId);
    this.emit();
  }

  async resolveThread(options: { threadId: string }) {
    this.apply(await setThreadResolved(options.threadId, true));
  }

  async unresolveThread(options: { threadId: string }) {
    this.apply(await setThreadResolved(options.threadId, false));
  }

  async addReaction(options: { threadId: string; commentId: string; emoji: string }) {
    this.apply(await setCommentReaction(options.commentId, options.emoji, true));
  }

  async deleteReaction(options: { threadId: string; commentId: string; emoji: string }) {
    this.apply(await setCommentReaction(options.commentId, options.emoji, false));
  }

  getThread(threadId: string) {
    const t = this.threads.get(threadId);
    if (!t) {
      throw new Error(`thread ${threadId} not found`);
    }
    return t;
  }

  getThreads() {
    return this.threads;
  }
}

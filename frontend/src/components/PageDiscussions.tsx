// Page-level discussions: threads with no anchor in the text. Anyone with
// comment access starts one; inline (anchored) comments are the editor's
// and need edit access, because the anchor is part of the document.
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, MessageSquare, RotateCcw, Trash2 } from 'lucide-react';
import {
  addThreadComment,
  atLeast,
  createThread,
  deleteThread,
  listThreads,
  peopleByIds,
  setThreadResolved,
  type Level,
  type RawThread,
} from '@/api/wiki';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { bodyToText, textToBody } from '@/lib/comments';
import { timeAgo } from '@/lib/format';
import { cn } from '@/lib/utils';

const fieldClasses =
  'w-full rounded-md border border-input bg-background px-3 py-2 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:border-primary';

export function PageDiscussions({ pageId, level }: { pageId: string; level: Level }) {
  const qc = useQueryClient();
  const canComment = atLeast(level, 'comment');
  const canEdit = atLeast(level, 'edit');
  const [draft, setDraft] = useState('');
  const threads = useQuery({
    queryKey: ['discussions', pageId],
    queryFn: ({ signal }) => listThreads(pageId, signal).then((all) => all.filter((t) => !t.anchored)),
    refetchInterval: 15_000,
  });
  const authorIds = [
    ...new Set((threads.data ?? []).flatMap((t) => t.comments.map((c) => c.user_id).filter((id): id is string => !!id))),
  ];
  const authors = useQuery({
    queryKey: ['people-by-id', authorIds.join(',')],
    queryFn: ({ signal }) => peopleByIds(authorIds, signal),
    enabled: authorIds.length > 0,
  });
  const nameOf = (id: string | null) => authors.data?.find((p) => p.id === id)?.display_name ?? 'Someone';
  const refresh = () => void qc.invalidateQueries({ queryKey: ['discussions', pageId] });

  const start = useMutation({
    mutationFn: (text: string) => createThread(pageId, textToBody(text), undefined, false),
    onSuccess: () => {
      setDraft('');
      refresh();
    },
  });

  const list = threads.data ?? [];
  if (!canComment && list.length === 0) return null;

  return (
    <section className="mt-8" aria-label="Discussion">
      <h2 className="font-heading font-bold mb-2 flex items-center gap-1.5">
        <MessageSquare className="w-4 h-4" /> Discussion
      </h2>
      {canComment && !canEdit ? (
        <p className="text-xs text-muted-foreground mb-2">
          You can comment on the page here. Comments on a piece of text need edit access.
        </p>
      ) : null}
      <div className="space-y-3">
        {list.map((t) => (
          <DiscussionThread
            key={t.id}
            thread={t}
            nameOf={nameOf}
            canComment={canComment}
            canEdit={canEdit}
            onChange={refresh}
          />
        ))}
      </div>
      {canComment ? (
        <form
          className="mt-3 space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (draft.trim()) start.mutate(draft.trim());
          }}
        >
          <textarea
            className={fieldClasses}
            rows={2}
            value={draft}
            placeholder="Start a discussion about this page"
            aria-label="New discussion"
            onChange={(e) => setDraft(e.target.value)}
          />
          <Button type="submit" size="sm" disabled={!draft.trim() || start.isPending}>
            Comment
          </Button>
          {start.isError ? <p className="text-sm text-destructive">{(start.error as Error).message}</p> : null}
        </form>
      ) : null}
    </section>
  );
}

function DiscussionThread({
  thread,
  nameOf,
  canComment,
  canEdit,
  onChange,
}: {
  thread: RawThread;
  nameOf: (id: string | null) => string;
  canComment: boolean;
  canEdit: boolean;
  onChange: () => void;
}) {
  const [reply, setReply] = useState('');
  const send = useMutation({
    mutationFn: (text: string) => addThreadComment(thread.id, textToBody(text)),
    onSuccess: () => {
      setReply('');
      onChange();
    },
  });
  const resolve = useMutation({ mutationFn: (r: boolean) => setThreadResolved(thread.id, r), onSuccess: onChange });
  const remove = useMutation({ mutationFn: () => deleteThread(thread.id), onSuccess: onChange });
  const error = send.error ?? resolve.error ?? remove.error;

  return (
    <Card className={cn('p-3 space-y-2', thread.resolved && 'opacity-70')}>
      {thread.comments.map((c) => (
        <div key={c.id} className="text-sm">
          <div className="text-xs text-muted-foreground">
            <span className="font-semibold text-foreground">{nameOf(c.user_id)}</span> · {timeAgo(c.created_at)}
          </div>
          {c.deleted_at ? (
            <p className="italic text-muted-foreground">Comment deleted</p>
          ) : (
            <p className="whitespace-pre-wrap">{bodyToText(c.body)}</p>
          )}
        </div>
      ))}
      <div className="flex flex-wrap items-center gap-2">
        {canComment ? (
          <form
            className="flex flex-1 gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (reply.trim()) send.mutate(reply.trim());
            }}
          >
            <input
              className={cn(fieldClasses, 'h-8 py-1')}
              value={reply}
              placeholder="Reply"
              aria-label="Reply"
              onChange={(e) => setReply(e.target.value)}
            />
            <Button type="submit" size="sm" variant="ghost" disabled={!reply.trim() || send.isPending}>
              Send
            </Button>
          </form>
        ) : null}
        {canComment ? (
          <Button
            size="sm"
            variant="ghost"
            disabled={resolve.isPending}
            onClick={() => resolve.mutate(!thread.resolved)}
          >
            {thread.resolved ? <RotateCcw className="w-3.5 h-3.5" /> : <CheckCircle2 className="w-3.5 h-3.5" />}
            {thread.resolved ? 'Reopen' : 'Resolve'}
          </Button>
        ) : null}
        {canEdit ? (
          <Button size="sm" variant="ghost" disabled={remove.isPending} onClick={() => remove.mutate()} aria-label="Delete discussion">
            <Trash2 className="w-3.5 h-3.5" />
          </Button>
        ) : null}
      </div>
      {error ? <p className="text-sm text-destructive">{(error as Error).message}</p> : null}
    </Card>
  );
}

// Public, read-only view of a shared page (and its sub-pages when the
// link includes them). No account, no sidebar chrome: just the content.
import { Suspense, lazy, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import * as Y from 'yjs';
import { FileText, Link2Off } from 'lucide-react';
import { getPublicShare, getPublicShareDoc, publicShareFileURL, type SharePage as ShareNode } from '@/api/wiki';
import { EmptyState, LoadingRow } from '@/components/ui/States';
import { cn } from '@/lib/utils';

const RevisionPreview = lazy(() => import('@/components/RevisionPreview'));

const FILE_PATH = /^\/api\/v1\/files\/([0-9a-f-]{36})$/i;

function ShareDoc({ token, pageId }: { token: string; pageId: string }) {
  const doc = useQuery({
    queryKey: ['share-doc', token, pageId],
    queryFn: () => getPublicShareDoc(token, pageId),
  });
  const merged = useMemo(
    () => (doc.data && doc.data.length > 0 ? Y.mergeUpdates(doc.data) : null),
    [doc.data]
  );
  const resolveFileUrl = useMemo(
    () => async (url: string) => {
      const m = FILE_PATH.exec(url);
      if (!m) return url;
      const res = await fetch(publicShareFileURL(token, m[1]));
      if (!res.ok) return '';
      return URL.createObjectURL(await res.blob());
    },
    [token]
  );
  if (doc.isLoading) return <LoadingRow />;
  if (doc.isError) return <p className="text-sm text-destructive">This page could not be loaded.</p>;
  if (!merged) return <p className="text-sm text-muted-foreground">This page is empty.</p>;
  return (
    <Suspense fallback={<LoadingRow />}>
      <RevisionPreview data={merged} resolveFileUrl={resolveFileUrl} />
    </Suspense>
  );
}

function TreeNav({
  pages,
  rootId,
  current,
  onPick,
}: {
  pages: ShareNode[];
  rootId: string;
  current: string;
  onPick: (id: string) => void;
}) {
  const children = (parent: string) => pages.filter((p) => p.parent_id === parent);
  const render = (nodes: ShareNode[], depth: number): React.ReactNode =>
    nodes.map((p) => (
      <div key={p.id}>
        <button
          type="button"
          style={{ paddingLeft: `${12 + depth * 14}px` }}
          className={cn(
            'w-full flex items-center gap-1.5 pr-2 py-1.5 rounded-md text-sm text-left hover:bg-secondary',
            current === p.id && 'bg-accent text-accent-foreground'
          )}
          onClick={() => onPick(p.id)}
        >
          <span className="w-4 text-center shrink-0">{p.icon || '📄'}</span>
          <span className="truncate">{p.title.trim() || 'Untitled'}</span>
        </button>
        {render(children(p.id), depth + 1)}
      </div>
    ));
  return <nav aria-label="Shared pages">{render(children(rootId), 0)}</nav>;
}

export function PublicSharePage() {
  const { token } = useParams({ from: '/share/$token' });
  const share = useQuery({
    queryKey: ['share', token],
    queryFn: () => getPublicShare(token),
    retry: false,
  });
  const [picked, setPicked] = useState<string | null>(null);

  if (share.isLoading) {
    return (
      <div className="min-h-screen bg-background text-foreground grid place-items-center">
        <LoadingRow />
      </div>
    );
  }
  if (share.isError || !share.data) {
    return (
      <div className="min-h-screen bg-background text-foreground grid place-items-center p-6">
        <EmptyState
          icon={Link2Off}
          title="This link is no longer available"
          description="It may have been revoked, expired, or the page was removed."
        />
      </div>
    );
  }

  const { page, pages } = share.data;
  const currentId = picked ?? page.id;
  const current = currentId === page.id ? page : (pages.find((p) => p.id === currentId) ?? page);

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col md:flex-row">
      {pages.length > 0 ? (
        <aside className="md:w-64 shrink-0 border-b md:border-b-0 md:border-r border-border p-3 md:min-h-screen">
          <button
            type="button"
            className={cn(
              'w-full flex items-center gap-1.5 px-2 py-1.5 rounded-md text-sm font-semibold text-left hover:bg-secondary',
              currentId === page.id && 'bg-accent text-accent-foreground'
            )}
            onClick={() => setPicked(page.id)}
          >
            <span className="w-4 text-center shrink-0">{page.icon || '📄'}</span>
            <span className="truncate">{page.title.trim() || 'Untitled'}</span>
          </button>
          <TreeNav pages={pages} rootId={page.id} current={currentId} onPick={setPicked} />
        </aside>
      ) : null}
      <main className="flex-1 min-w-0 px-6 py-10 max-w-3xl mx-auto w-full">
        <div className="mb-8 space-y-2">
          {current.icon ? <div className="text-5xl leading-none">{current.icon}</div> : null}
          <h1 className="font-heading text-4xl font-extrabold tracking-tight">
            {current.title.trim() || 'Untitled'}
          </h1>
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <FileText className="w-3.5 h-3.5" /> Shared read-only
          </p>
        </div>
        <ShareDoc token={token} pageId={currentId} />
      </main>
    </div>
  );
}

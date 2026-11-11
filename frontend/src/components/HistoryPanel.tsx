// Page history: the revision list, a read-only preview and restore.
// Revisions are written by the backend at most every 10 minutes of
// editing, plus one for the state replaced by every restore.
import { Suspense, lazy, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { History, RotateCcw } from 'lucide-react';
import { fetchRevisionDoc, listRevisions, restoreRevision, type Page, type Revision } from '@/api/wiki';
import { Button } from '@/components/ui/Button';
import { Dialog } from '@/components/ui/Dialog';
import { EmptyState, LoadingRow } from '@/components/ui/States';
import { timeAgo } from '@/lib/format';
import { cn } from '@/lib/utils';

const RevisionPreview = lazy(() => import('@/components/RevisionPreview'));

const REASON_LABELS: Record<Revision['reason'], string> = {
  auto: 'Autosaved',
  restore: 'Before a restore',
  manual: 'Saved',
};

function sizeLabel(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function HistoryPanel({ page, canEdit, onClose }: { page: Page; canEdit: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [selected, setSelected] = useState<Revision | null>(null);
  const revisions = useQuery({
    queryKey: ['revisions', page.id],
    queryFn: ({ signal }) => listRevisions(page.id, signal),
  });
  const preview = useQuery({
    queryKey: ['revision-doc', selected?.id],
    queryFn: () => fetchRevisionDoc(selected!.id),
    enabled: selected !== null,
    staleTime: Infinity, // revisions never change
  });
  const restore = useMutation({
    mutationFn: (revisionId: string) => restoreRevision(revisionId),
    onSuccess: () => {
      // The live provider reloads on its own (the server closes the
      // socket); the list gains the "before a restore" entry.
      void qc.invalidateQueries({ queryKey: ['revisions', page.id] });
      onClose();
    },
  });

  return (
    <Dialog title="Page history" onClose={onClose} className="max-w-3xl">
      {revisions.isLoading ? <LoadingRow /> : null}
      {revisions.isError ? <p className="text-sm text-destructive">{(revisions.error as Error).message}</p> : null}
      {revisions.data && revisions.data.revisions.length === 0 ? (
        <EmptyState
          icon={History}
          title="No revisions yet"
          description="A revision is kept for every ~10 minutes of editing, and before every restore."
        />
      ) : null}
      {revisions.data && revisions.data.revisions.length > 0 ? (
        <div className="flex gap-4 min-h-[18rem]">
          <ul className="w-56 shrink-0 space-y-1 overflow-y-auto max-h-[60vh]" aria-label="Revisions">
            {revisions.data.revisions.map((rev) => (
              <li key={rev.id}>
                <button
                  type="button"
                  onClick={() => setSelected(rev)}
                  className={cn(
                    'w-full rounded-md px-3 py-2 text-left text-sm hover:bg-secondary',
                    selected?.id === rev.id && 'bg-secondary'
                  )}
                >
                  <span className="block font-medium">{timeAgo(rev.created_at)}</span>
                  <span className="block text-xs text-muted-foreground">
                    {REASON_LABELS[rev.reason]} · {sizeLabel(rev.size_bytes)}
                  </span>
                </button>
              </li>
            ))}
          </ul>
          <div className="flex-1 min-w-0 border-l border-border pl-4">
            {selected === null ? (
              <p className="text-sm text-muted-foreground">Pick a revision to preview it.</p>
            ) : (
              <>
                <div className="flex items-center gap-2 mb-3">
                  <span className="text-sm text-muted-foreground flex-1">
                    {new Date(selected.created_at).toLocaleString()}
                  </span>
                  {canEdit ? (
                    <Button
                      size="sm"
                      disabled={restore.isPending}
                      onClick={() => restore.mutate(selected.id)}
                    >
                      <RotateCcw className="w-3.5 h-3.5" /> Restore this version
                    </Button>
                  ) : null}
                </div>
                {restore.isError ? (
                  <p className="text-sm text-destructive mb-2">{(restore.error as Error).message}</p>
                ) : null}
                <div className="overflow-y-auto max-h-[55vh]">
                  {preview.isLoading ? <LoadingRow /> : null}
                  {preview.isError ? (
                    <p className="text-sm text-destructive">{(preview.error as Error).message}</p>
                  ) : null}
                  {preview.data ? (
                    <Suspense fallback={<LoadingRow />}>
                      <RevisionPreview data={preview.data} />
                    </Suspense>
                  ) : null}
                </div>
              </>
            )}
          </div>
        </div>
      ) : null}
      {revisions.data && revisions.data.retention_days > 0 ? (
        <p className="text-xs text-muted-foreground">
          Revisions older than {revisions.data.retention_days} days are deleted
          {revisions.data.retention_source === 'den' ? ' (set in Den)' : ''}.
        </p>
      ) : null}
    </Dialog>
  );
}

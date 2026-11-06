import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { FileText, RotateCcw, Trash2 } from 'lucide-react';
import { deletePage, getSpace, getTrash, restorePage } from '@/api/wiki';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { PageContainer, PageHeader } from '@/components/ui/PageHeader';
import { EmptyState, ErrorRow, LoadingRow } from '@/components/ui/States';
import { timeAgo } from '@/lib/format';

export function TrashPage() {
  const { spaceId } = useParams({ from: '/app/s/$spaceId/trash' });
  const qc = useQueryClient();
  const space = useQuery({ queryKey: ['space', spaceId], queryFn: ({ signal }) => getSpace(spaceId, signal) });
  const trash = useQuery({ queryKey: ['trash', spaceId], queryFn: ({ signal }) => getTrash(spaceId, signal) });

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['trash', spaceId] });
    void qc.invalidateQueries({ queryKey: ['tree', spaceId] });
  };
  const restore = useMutation({ mutationFn: restorePage, onSuccess: refresh });
  const remove = useMutation({ mutationFn: deletePage, onSuccess: refresh });
  const error = restore.error ?? remove.error;

  return (
    <PageContainer className="max-w-4xl">
      <PageHeader
        title={`Trash${space.data ? ` · ${space.data.name}` : ''}`}
        description="Pages you trashed or could trash. Restoring brings back everything that was inside."
      />
      {trash.isLoading ? <LoadingRow /> : null}
      {trash.isError ? <ErrorRow message={(trash.error as Error).message} /> : null}
      {trash.isSuccess && trash.data.length === 0 ? <EmptyState icon={Trash2} title="Trash is empty" /> : null}
      {trash.data && trash.data.length > 0 ? (
        <Card className="divide-y divide-border">
          {trash.data.map((p) => (
            <div key={p.id} className="flex items-center gap-3 px-4 py-3 text-sm">
              {p.icon ? <span>{p.icon}</span> : <FileText className="w-4 h-4 text-muted-foreground" />}
              <span className="flex-1 min-w-0">
                <span className="block truncate font-medium">{p.title.trim() || 'Untitled'}</span>
                <span className="text-xs text-muted-foreground">Trashed {timeAgo(p.deleted_at)}</span>
              </span>
              <Button size="sm" variant="secondary" disabled={restore.isPending} onClick={() => restore.mutate(p.id)}>
                <RotateCcw className="w-3.5 h-3.5" /> Restore
              </Button>
              <Button
                size="sm"
                variant="ghost"
                className="text-destructive"
                disabled={remove.isPending}
                onClick={() => {
                  if (window.confirm('Delete this page and everything inside it forever?')) remove.mutate(p.id);
                }}
              >
                Delete forever
              </Button>
            </div>
          ))}
        </Card>
      ) : null}
      {error ? <p className="text-sm text-destructive mt-3">{(error as Error).message}</p> : null}
    </PageContainer>
  );
}

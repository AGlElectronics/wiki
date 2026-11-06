import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { FileText, LayoutTemplate, Plus, Settings, Share2, Table2, Trash2, Upload } from 'lucide-react';
import { atLeast, createPage, duplicatePage, getSpace, type PageNode } from '@/api/wiki';
import { useCreatePage, useTree } from '@/components/PageTree';
import { ShareDialog } from '@/components/ShareDialog';
import { SpaceDialog } from '@/components/SpaceDialog';
import { Button, buttonClasses } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { PageContainer } from '@/components/ui/PageHeader';
import { EmptyState, ErrorRow, LoadingRow } from '@/components/ui/States';
import { importMarkdownFile, pickMarkdownFile } from '@/lib/markdown';

/** "New from template" dropdown: every template in the space. */
function TemplateMenu({ spaceId, templates }: { spaceId: string; templates: PageNode[] }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  if (templates.length === 0) return null;
  return (
    <div className="relative">
      <Button variant="secondary" disabled={busy} aria-expanded={open} onClick={() => setOpen(!open)}>
        <LayoutTemplate className="w-4 h-4" /> New from template
      </Button>
      {open ? (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute right-0 z-50 mt-1 w-64 rounded-md border border-border bg-card py-1 shadow-pop">
            {templates.map((t) => (
              <button
                key={t.id}
                type="button"
                className="w-full flex items-center gap-2 px-3 py-2 text-left text-sm hover:bg-secondary"
                onClick={() => {
                  setOpen(false);
                  setBusy(true);
                  duplicatePage(t.id, { parent_id: null })
                    .then(({ page }) => {
                      void qc.invalidateQueries({ queryKey: ['tree', spaceId] });
                      return navigate({ to: '/p/$pageId', params: { pageId: page.id } });
                    })
                    .finally(() => setBusy(false));
                }}
              >
                <span className="w-4 text-center shrink-0">{t.icon || '📄'}</span>
                <span className="truncate">{t.title.trim() || 'Untitled'}</span>
              </button>
            ))}
          </div>
        </>
      ) : null}
    </div>
  );
}

export function SpacePage() {
  const { spaceId } = useParams({ from: '/app/s/$spaceId' });
  const qc = useQueryClient();
  const navigate = useNavigate();
  const space = useQuery({ queryKey: ['space', spaceId], queryFn: ({ signal }) => getSpace(spaceId, signal) });
  const tree = useTree(spaceId);
  const create = useCreatePage(spaceId);
  const [dialog, setDialog] = useState<'share' | 'settings' | null>(null);

  if (space.isLoading) return <LoadingRow />;
  if (space.isError) return <ErrorRow message={(space.error as Error).message} />;
  const sp = space.data!;
  const canEdit = atLeast(sp.access, 'edit');
  const full = atLeast(sp.access, 'full');
  const roots = (tree.data ?? []).filter((p) => p.parent_id === null);
  const templates = (tree.data ?? []).filter((p) => p.is_template);

  const importMarkdown = () => {
    void pickMarkdownFile().then(async (file) => {
      if (!file) return;
      const pageId = await importMarkdownFile(spaceId, null, file);
      void qc.invalidateQueries({ queryKey: ['tree', spaceId] });
      await navigate({ to: '/p/$pageId', params: { pageId } });
    });
  };

  return (
    <PageContainer className="max-w-4xl">
      {sp.orphaned_at ? (
        <div className="mb-4 rounded-md border border-warning/50 bg-warning/10 px-4 py-3 text-sm">
          This private space belonged to someone who was offboarded on{' '}
          {new Date(sp.orphaned_at).toLocaleDateString()}. It will be deleted automatically after the grace
          period — export anything worth keeping now.
        </div>
      ) : null}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between mb-6">
        <div className="flex items-center gap-3 min-w-0">
          <span className="text-4xl">{sp.icon || '📚'}</span>
          <div className="min-w-0">
            <h1 className="font-heading text-2xl font-extrabold leading-tight tracking-tight truncate">{sp.name}</h1>
            {sp.description ? <p className="text-sm text-muted-foreground mt-1">{sp.description}</p> : null}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {canEdit ? (
            <Link to="/s/$spaceId/trash" params={{ spaceId }} className={buttonClasses({ variant: 'ghost' })}>
              <Trash2 className="w-4 h-4" /> Trash
            </Link>
          ) : null}
          {full && sp.kind === 'team' ? (
            <Button variant="secondary" onClick={() => setDialog('share')}>
              <Share2 className="w-4 h-4" /> Share
            </Button>
          ) : null}
          {full ? (
            <Button variant="secondary" size="icon" aria-label="Space settings" onClick={() => setDialog('settings')}>
              <Settings className="w-4 h-4" />
            </Button>
          ) : null}
          {canEdit ? (
            <Button variant="secondary" aria-label="Import Markdown" onClick={importMarkdown}>
              <Upload className="w-4 h-4" /> Import
            </Button>
          ) : null}
          {canEdit ? <TemplateMenu spaceId={spaceId} templates={templates} /> : null}
          {canEdit ? (
            <Button
              variant="secondary"
              onClick={() => {
                void createPage(spaceId, { parent_id: null, title: '', kind: 'database' }).then((page) => {
                  void qc.invalidateQueries({ queryKey: ['tree', spaceId] });
                  return navigate({ to: '/p/$pageId', params: { pageId: page.id } });
                });
              }}
            >
              <Table2 className="w-4 h-4" /> New database
            </Button>
          ) : null}
          {canEdit ? (
            <Button disabled={create.isPending} onClick={() => create.mutate(null)}>
              <Plus className="w-4 h-4" /> New page
            </Button>
          ) : null}
        </div>
      </div>

      {tree.isLoading ? <LoadingRow /> : null}
      {tree.isSuccess && roots.length === 0 ? (
        <EmptyState
          icon={FileText}
          title="No pages yet"
          description={canEdit ? 'Create the first page of this space.' : 'Nothing has been written here yet.'}
        />
      ) : null}
      {roots.length > 0 ? (
        <Card className="divide-y divide-border">
          {roots.map((p) => (
            <Link
              key={p.id}
              to="/p/$pageId"
              params={{ pageId: p.id }}
              className="flex items-center gap-3 px-4 py-3 text-sm hover:bg-secondary"
            >
              {p.icon ? <span>{p.icon}</span> : <FileText className="w-4 h-4 text-muted-foreground" />}
              <span className="flex-1 truncate font-medium">{p.title.trim() || 'Untitled'}</span>
            </Link>
          ))}
        </Card>
      ) : null}

      {dialog === 'share' ? <ShareDialog target={{ kind: 'space', space: sp }} onClose={() => setDialog(null)} /> : null}
      {dialog === 'settings' ? <SpaceDialog space={sp} onClose={() => setDialog(null)} /> : null}
    </PageContainer>
  );
}

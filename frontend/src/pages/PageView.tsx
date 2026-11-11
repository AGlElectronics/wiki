import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import {
  ChevronRight,
  Copy,
  Download,
  FileText,
  History,
  LayoutTemplate,
  Link2,
  Lock,
  MoreHorizontal,
  Plus,
  Share2,
  Star,
  Trash2,
  Upload,
} from 'lucide-react';
import {
  addFavorite,
  atLeast,
  duplicatePage,
  getBacklinks,
  getPage,
  listFavorites,
  removeFavorite,
  trashPage,
  updatePage,
  type Page,
} from '@/api/wiki';
import { DatabaseView } from '@/components/DatabaseView';
import { HistoryPanel } from '@/components/HistoryPanel';
import { downloadText, importMarkdownFile, pickMarkdownFile } from '@/lib/markdown';
import { LiveStatusText, PageEditor, Presence, useLivePage } from '@/components/PageEditor';
import { PageDiscussions } from '@/components/PageDiscussions';
import { useCreatePage, useTree } from '@/components/PageTree';
import { ShareDialog } from '@/components/ShareDialog';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { PageContainer } from '@/components/ui/PageHeader';
import { EmptyState, LoadingRow } from '@/components/ui/States';
import { timeAgo } from '@/lib/format';

export function PageView() {
  const { pageId } = useParams({ from: '/app/p/$pageId' });
  const page = useQuery({ queryKey: ['page', pageId], queryFn: ({ signal }) => getPage(pageId, signal), retry: false });

  if (page.isLoading) return <LoadingRow />;
  if (page.isError) {
    return (
      <EmptyState
        icon={FileText}
        title="Page not found"
        description="It may have been moved to the trash, or you no longer have access to it."
      />
    );
  }
  return <PageBody key={pageId} page={page.data!} />;
}

function Breadcrumbs({ page }: { page: Page }) {
  return (
    <nav className="flex items-center flex-wrap gap-1 text-sm text-muted-foreground min-w-0" aria-label="Breadcrumbs">
      <Link to="/s/$spaceId" params={{ spaceId: page.space_id }} className="hover:text-foreground truncate">
        {page.space_name}
      </Link>
      {page.breadcrumbs.map((c) => (
        <span key={c.id} className="flex items-center gap-1 min-w-0">
          <ChevronRight className="w-3.5 h-3.5 shrink-0" />
          <Link to="/p/$pageId" params={{ pageId: c.id }} className="hover:text-foreground truncate">
            {c.icon ? `${c.icon} ` : ''}
            {c.title.trim() || 'Untitled'}
          </Link>
        </span>
      ))}
    </nav>
  );
}

function FavoriteButton({ pageId }: { pageId: string }) {
  const qc = useQueryClient();
  const favorites = useQuery({ queryKey: ['favorites'], queryFn: ({ signal }) => listFavorites(signal) });
  const starred = (favorites.data ?? []).some((f) => f.page_id === pageId);
  const toggle = useMutation({
    mutationFn: () => (starred ? removeFavorite(pageId) : addFavorite(pageId)),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['favorites'] }),
  });
  return (
    <Button
      variant="ghost"
      size="sm"
      aria-label={starred ? 'Remove from favourites' : 'Add to favourites'}
      aria-pressed={starred}
      disabled={toggle.isPending || favorites.isLoading}
      onClick={() => toggle.mutate()}
    >
      <Star className={starred ? 'w-3.5 h-3.5 fill-current text-warning' : 'w-3.5 h-3.5'} />
    </Button>
  );
}

function Backlinks({ pageId }: { pageId: string }) {
  const backlinks = useQuery({
    queryKey: ['backlinks', pageId],
    queryFn: ({ signal }) => getBacklinks(pageId, signal),
  });
  if (!backlinks.data || backlinks.data.length === 0) return null;
  return (
    <section className="mt-8">
      <h2 className="font-heading font-bold mb-2 flex items-center gap-1.5">
        <Link2 className="w-4 h-4" /> Linked from
      </h2>
      <Card className="divide-y divide-border">
        {backlinks.data.map((hit) => (
          <Link
            key={hit.id}
            to="/p/$pageId"
            params={{ pageId: hit.id }}
            className="flex items-center gap-3 px-4 py-2.5 text-sm hover:bg-secondary"
          >
            {hit.icon ? <span>{hit.icon}</span> : <FileText className="w-4 h-4 text-muted-foreground" />}
            <span className="flex-1 truncate font-medium">{hit.title.trim() || 'Untitled'}</span>
            <span className="text-xs text-muted-foreground truncate">{hit.space_name}</span>
          </Link>
        ))}
      </Card>
    </section>
  );
}

/** Overflow menu: duplicate, template flag, markdown export and import. */
function PageMenu({
  page,
  canEdit,
  exportRef,
}: {
  page: Page;
  canEdit: boolean;
  exportRef: React.MutableRefObject<(() => string) | null>;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = (fn: () => Promise<void>) => {
    setOpen(false);
    fn().catch((e: Error) => setError(e.message));
  };
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['tree', page.space_id] });
    void qc.invalidateQueries({ queryKey: ['page', page.id] });
  };

  const item =
    'w-full flex items-center gap-2 px-3 py-2 text-left text-sm hover:bg-secondary disabled:opacity-50';
  return (
    <div className="relative">
      <Button variant="ghost" size="sm" aria-label="Page menu" aria-expanded={open} onClick={() => setOpen(!open)}>
        <MoreHorizontal className="w-3.5 h-3.5" />
      </Button>
      {open ? (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute right-0 z-50 mt-1 w-64 rounded-md border border-border bg-card py-1 shadow-pop">
            {canEdit ? (
              <button
                type="button"
                className={item}
                onClick={() =>
                  run(async () => {
                    const { page: copy } = await duplicatePage(page.id, { parent_id: page.parent_id });
                    invalidate();
                    await navigate({ to: '/p/$pageId', params: { pageId: copy.id } });
                  })
                }
              >
                <Copy className="w-4 h-4" /> Duplicate
              </button>
            ) : null}
            {canEdit ? (
              <button
                type="button"
                className={item}
                onClick={() =>
                  run(async () => {
                    await updatePage(page.id, { is_template: !page.is_template });
                    invalidate();
                  })
                }
              >
                <LayoutTemplate className="w-4 h-4" />
                {page.is_template ? 'Remove from templates' : 'Mark as template'}
              </button>
            ) : null}
            <button
              type="button"
              className={item}
              onClick={() =>
                run(async () => {
                  const exporter = exportRef.current;
                  if (!exporter) throw new Error('The page is still loading.');
                  downloadText(`${page.title.trim() || 'page'}.md`, exporter());
                })
              }
            >
              <Download className="w-4 h-4" /> Export as Markdown
            </button>
            {canEdit ? (
              <button
                type="button"
                className={item}
                onClick={() =>
                  run(async () => {
                    const file = await pickMarkdownFile();
                    if (!file) return;
                    const newID = await importMarkdownFile(page.space_id, page.id, file);
                    invalidate();
                    await navigate({ to: '/p/$pageId', params: { pageId: newID } });
                  })
                }
              >
                <Upload className="w-4 h-4" /> Import Markdown as sub-page
              </button>
            ) : null}
          </div>
        </>
      ) : null}
      {error ? <p className="absolute right-0 mt-1 text-xs text-destructive whitespace-nowrap">{error}</p> : null}
    </div>
  );
}

function PageBody({ page }: { page: Page }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [sharing, setSharing] = useState(false);
  const [history, setHistory] = useState(false);
  const [title, setTitle] = useState(page.title);
  const exportRef = useRef<(() => string) | null>(null);
  const [icon, setIcon] = useState(page.icon);
  const canEdit = atLeast(page.access, 'edit');
  const tree = useTree(page.space_id);
  const create = useCreatePage(page.space_id);
  const children = (tree.data ?? []).filter((p) => p.parent_id === page.id);
  const live = useLivePage(page.id);

  const save = useMutation({
    mutationFn: (body: { title?: string; icon?: string }) => updatePage(page.id, body),
    onSuccess: (saved) => {
      qc.setQueryData(['page', page.id], saved);
      void qc.invalidateQueries({ queryKey: ['tree', page.space_id] });
    },
  });
  const trash = useMutation({
    mutationFn: () => trashPage(page.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['tree', page.space_id] });
      void qc.invalidateQueries({ queryKey: ['trash', page.space_id] });
      const parent = page.breadcrumbs.at(-1);
      if (parent) void navigate({ to: '/p/$pageId', params: { pageId: parent.id } });
      else void navigate({ to: '/s/$spaceId', params: { spaceId: page.space_id } });
    },
  });

  // Save the title shortly after typing stops.
  const saveRef = useRef(save.mutate);
  saveRef.current = save.mutate;
  useEffect(() => {
    if (title === page.title) return;
    const t = window.setTimeout(() => saveRef.current({ title }), 500);
    return () => window.clearTimeout(t);
  }, [title, page.title]);

  return (
    <PageContainer className="max-w-4xl">
      <div className="flex items-center gap-3 mb-8">
        <Breadcrumbs page={page} />
        <div className="flex-1" />
        {live.provider ? <Presence provider={live.provider} /> : null}
        {live.state ? (
          <LiveStatusText state={live.state} />
        ) : (
          <span className="hidden sm:inline text-xs text-muted-foreground whitespace-nowrap">
            Edited {timeAgo(page.updated_at)}
          </span>
        )}
        {page.restricted ? <Lock className="w-4 h-4 text-muted-foreground" aria-label="Restricted page" /> : null}
        <FavoriteButton pageId={page.id} />
        <Button variant="ghost" size="sm" aria-label="Page history" onClick={() => setHistory(true)}>
          <History className="w-3.5 h-3.5" />
        </Button>
        {atLeast(page.access, 'full') ? (
          <Button variant="secondary" size="sm" onClick={() => setSharing(true)}>
            <Share2 className="w-3.5 h-3.5" /> Share
          </Button>
        ) : null}
        {page.can_trash ? (
          <Button
            variant="ghost"
            size="sm"
            aria-label="Move to trash"
            disabled={trash.isPending}
            onClick={() => trash.mutate()}
          >
            <Trash2 className="w-3.5 h-3.5" />
          </Button>
        ) : null}
        <PageMenu page={page} canEdit={canEdit} exportRef={exportRef} />
      </div>

      <div className="space-y-2 mb-8">
        {canEdit ? (
          <input
            value={icon}
            placeholder="📄"
            maxLength={8}
            aria-label="Page icon"
            className="block w-16 bg-transparent text-5xl leading-none outline-none placeholder:opacity-30"
            onChange={(e) => setIcon(e.target.value)}
            onBlur={() => {
              if (icon !== page.icon) save.mutate({ icon: icon.trim() });
            }}
          />
        ) : page.icon ? (
          <div className="text-5xl leading-none">{page.icon}</div>
        ) : null}
        {canEdit ? (
          <input
            value={title}
            placeholder="Untitled"
            aria-label="Page title"
            className="block w-full bg-transparent font-heading text-4xl font-extrabold tracking-tight outline-none placeholder:text-muted-foreground/50"
            onChange={(e) => setTitle(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') e.currentTarget.blur();
            }}
          />
        ) : (
          <h1 className="font-heading text-4xl font-extrabold tracking-tight">{page.title.trim() || 'Untitled'}</h1>
        )}
        {save.isError ? <p className="text-sm text-destructive">{(save.error as Error).message}</p> : null}
        {trash.isError ? <p className="text-sm text-destructive">{(trash.error as Error).message}</p> : null}
      </div>

      <div className="mb-12 min-h-[30vh]">
        {live.provider && live.state ? (
          <PageEditor provider={live.provider} state={live.state} exportRef={exportRef} />
        ) : (
          <LoadingRow />
        )}
      </div>

      {page.kind === 'database' ? <DatabaseView page={page} canEdit={canEdit} /> : null}

      {page.kind !== 'database' && (children.length > 0 || canEdit) ? (
        <section>
          <div className="flex items-center justify-between mb-2">
            <h2 className="font-heading font-bold">Sub-pages</h2>
            {canEdit ? (
              <Button variant="ghost" size="sm" disabled={create.isPending} onClick={() => create.mutate(page.id)}>
                <Plus className="w-3.5 h-3.5" /> Add a sub-page
              </Button>
            ) : null}
          </div>
          {children.length > 0 ? (
            <Card className="divide-y divide-border">
              {children.map((p) => (
                <Link
                  key={p.id}
                  to="/p/$pageId"
                  params={{ pageId: p.id }}
                  className="flex items-center gap-3 px-4 py-3 text-sm hover:bg-secondary"
                >
                  {p.icon ? <span>{p.icon}</span> : <FileText className="w-4 h-4 text-muted-foreground" />}
                  <span className="flex-1 truncate font-medium">{p.title.trim() || 'Untitled'}</span>
                  {p.restricted ? <Lock className="w-3.5 h-3.5 text-muted-foreground" /> : null}
                </Link>
              ))}
            </Card>
          ) : null}
        </section>
      ) : null}

      <PageDiscussions pageId={page.id} level={page.access} />

      <Backlinks pageId={page.id} />

      {sharing ? <ShareDialog target={{ kind: 'page', page }} onClose={() => setSharing(false)} /> : null}
      {history ? <HistoryPanel page={page} canEdit={canEdit} onClose={() => setHistory(false)} /> : null}
    </PageContainer>
  );
}

import { useMemo, useState, useSyncExternalStore } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useRouterState } from '@tanstack/react-router';
import { ChevronRight, FileText, Lock, Plus } from 'lucide-react';
import { atLeast, createPage, getTree, movePage, type PageNode } from '@/api/wiki';
import { cn } from '@/lib/utils';

// Expanded tree nodes survive reloads, like Notion's sidebar.
const STORAGE_KEY = 'wiki.sidebar.expanded';
let expanded = new Set<string>(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]') as string[]);
const listeners = new Set<() => void>();

function setExpanded(id: string, open: boolean) {
  if (expanded.has(id) === open) return;
  expanded = new Set(expanded);
  if (open) expanded.add(id);
  else expanded.delete(id);
  localStorage.setItem(STORAGE_KEY, JSON.stringify([...expanded]));
  listeners.forEach((l) => l());
}

export function useExpanded(id: string): [boolean, (open: boolean) => void] {
  const set = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => expanded
  );
  return [set.has(id), (open) => setExpanded(id, open)];
}

const DRAG_TYPE = 'application/x-wiki-page';

export function useTree(spaceId: string) {
  return useQuery({ queryKey: ['tree', spaceId], queryFn: ({ signal }) => getTree(spaceId, signal) });
}

/** Creates a page (optionally under a parent) and opens it. */
export function useCreatePage(spaceId: string) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useMutation({
    mutationFn: (parentId: string | null) => createPage(spaceId, { parent_id: parentId }),
    onSuccess: (page, parentId) => {
      if (parentId) setExpanded(parentId, true);
      void qc.invalidateQueries({ queryKey: ['tree', spaceId] });
      void navigate({ to: '/p/$pageId', params: { pageId: page.id } });
    },
  });
}

/** Moves a dragged page under `parentId` (null = space root), as its last child. */
export function useMovePage(spaceId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ pageId, parentId }: { pageId: string; parentId: string | null }) =>
      movePage(pageId, { parent_id: parentId }),
    onSuccess: (page, { parentId }) => {
      if (parentId) setExpanded(parentId, true);
      void qc.invalidateQueries({ queryKey: ['tree', spaceId] });
      void qc.invalidateQueries({ queryKey: ['page', page.id] });
    },
    onError: (err) => window.alert((err as Error).message),
  });
}

/** Drop handlers that move a page from this space under `parentId`. */
export function dropTarget(
  spaceId: string,
  parentId: string | null,
  move: ReturnType<typeof useMovePage>,
  setOver: (over: boolean) => void
) {
  return {
    onDragOver: (e: React.DragEvent) => {
      if (!e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      setOver(true);
    },
    onDragLeave: () => setOver(false),
    onDrop: (e: React.DragEvent) => {
      setOver(false);
      const raw = e.dataTransfer.getData(DRAG_TYPE);
      if (!raw) return;
      e.preventDefault();
      e.stopPropagation();
      const dragged = JSON.parse(raw) as { id: string; spaceId: string };
      if (dragged.spaceId !== spaceId || dragged.id === parentId) return;
      move.mutate({ pageId: dragged.id, parentId });
    },
  };
}

export function PageTree({
  spaceId,
  onNavigate,
  emptyHint,
}: {
  spaceId: string;
  onNavigate?: () => void;
  emptyHint?: string;
}) {
  const tree = useTree(spaceId);
  const children = useMemo(() => {
    const map = new Map<string | null, PageNode[]>();
    for (const p of tree.data ?? []) {
      const list = map.get(p.parent_id) ?? [];
      list.push(p);
      map.set(p.parent_id, list);
    }
    return map;
  }, [tree.data]);

  const roots = children.get(null) ?? [];
  if (tree.isSuccess && roots.length === 0 && emptyHint) {
    return <div className="pl-9 pr-3 py-1 text-xs text-muted-foreground">{emptyHint}</div>;
  }
  return (
    <div>
      {roots.map((p) => (
        <TreeNode key={p.id} page={p} depth={0} spaceId={spaceId} childrenOf={children} onNavigate={onNavigate} />
      ))}
    </div>
  );
}

function TreeNode({
  page,
  depth,
  spaceId,
  childrenOf,
  onNavigate,
}: {
  page: PageNode;
  depth: number;
  spaceId: string;
  childrenOf: Map<string | null, PageNode[]>;
  onNavigate?: () => void;
}) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const [open, setOpen] = useExpanded(page.id);
  const [over, setOver] = useState(false);
  const create = useCreatePage(spaceId);
  const move = useMovePage(spaceId);
  const kids = childrenOf.get(page.id) ?? [];
  const active = pathname === `/p/${page.id}`;
  const canEdit = atLeast(page.access, 'edit');

  return (
    <div>
      <div
        className={cn(
          'group flex items-center gap-1 pr-1 py-0.5 rounded-md text-sm transition-colors',
          active ? 'bg-accent text-accent-foreground font-medium' : 'text-muted-foreground hover:bg-secondary hover:text-foreground',
          over && 'ring-2 ring-primary'
        )}
        style={{ paddingLeft: 8 + depth * 14 }}
        draggable={canEdit}
        onDragStart={(e) => {
          e.dataTransfer.setData(DRAG_TYPE, JSON.stringify({ id: page.id, spaceId }));
          e.dataTransfer.effectAllowed = 'move';
        }}
        {...dropTarget(spaceId, page.id, move, setOver)}
      >
        <button
          type="button"
          className={cn('p-0.5 rounded hover:bg-background/60', kids.length === 0 && 'invisible')}
          aria-label={open ? 'Collapse' : 'Expand'}
          onClick={() => setOpen(!open)}
        >
          <ChevronRight className={cn('w-3.5 h-3.5 transition-transform', open && 'rotate-90')} />
        </button>
        <Link
          to="/p/$pageId"
          params={{ pageId: page.id }}
          onClick={onNavigate}
          className="flex-1 min-w-0 flex items-center gap-1.5 py-1"
        >
          {page.icon ? (
            <span className="w-4 text-center shrink-0">{page.icon}</span>
          ) : (
            <FileText className="w-4 h-4 shrink-0" />
          )}
          <span className="truncate">{page.title.trim() || 'Untitled'}</span>
          {page.restricted ? <Lock className="w-3 h-3 shrink-0 opacity-60" aria-label="Restricted" /> : null}
        </Link>
        {canEdit ? (
          <button
            type="button"
            className="p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-background/60 focus-visible:opacity-100"
            aria-label="Add a page inside"
            disabled={create.isPending}
            onClick={() => create.mutate(page.id)}
          >
            <Plus className="w-3.5 h-3.5" />
          </button>
        ) : null}
      </div>
      {open
        ? kids.map((k) => (
            <TreeNode
              key={k.id}
              page={k}
              depth={depth + 1}
              spaceId={spaceId}
              childrenOf={childrenOf}
              onNavigate={onNavigate}
            />
          ))
        : null}
    </div>
  );
}

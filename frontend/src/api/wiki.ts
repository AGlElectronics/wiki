import { ApiError, apiBlob, apiRequest, apiUpload, apiURL } from '@/api/client';

/** Access levels, lowest first. Mirrors domain.Level in the backend. */
export const LEVELS = ['none', 'view', 'comment', 'edit', 'full'] as const;
export type Level = (typeof LEVELS)[number];
export type GrantLevel = Exclude<Level, 'none'>;

export const LEVEL_LABELS: Record<GrantLevel, string> = {
  view: 'Can view',
  comment: 'Can comment',
  edit: 'Can edit',
  full: 'Full access',
};

export function atLeast(have: Level, want: Level): boolean {
  return LEVELS.indexOf(have) >= LEVELS.indexOf(want);
}

export type PrincipalKind = 'user' | 'group';

export type Grant = {
  principal_kind: PrincipalKind;
  principal: string;
  level: GrantLevel;
  display_name: string;
};

export type Space = {
  id: string;
  kind: 'private' | 'team';
  name: string;
  icon: string;
  description: string;
  everyone_level: GrantLevel | null;
  created_at: string;
  access: Level;
  /** Set when the owner was offboarded; admins see it until the sweep. */
  orphaned_at: string | null;
  grants: Grant[];
};

export type PageNode = {
  id: string;
  parent_id: string | null;
  position: number;
  title: string;
  icon: string;
  kind: 'page' | 'database';
  restricted: boolean;
  is_template: boolean;
  access: Level;
};

export type Crumb = { id: string; title: string; icon: string };

export type Page = PageNode & {
  space_id: string;
  space_name: string;
  space_kind: 'private' | 'team';
  updated_at: string;
  breadcrumbs: Crumb[];
  grants: Grant[];
  can_trash: boolean;
};

export type SharedPage = PageNode & { space_id: string; space_name: string };

export type TrashedPage = { id: string; title: string; icon: string; deleted_at: string };

export type Person = { id: string; display_name: string; email?: string };

export type SpaceList = { spaces: Space[]; shared: SharedPage[]; can_create: boolean };

export type SpaceInput = {
  name?: string;
  icon?: string;
  description?: string;
  /** '' closes the space to everyone not granted explicitly. */
  everyone_level?: GrantLevel | '';
};

/** level '' removes the grant. */
export type GrantInput = { principal_kind: PrincipalKind; principal: string; level: GrantLevel | '' };

export const listSpaces = (signal?: AbortSignal) => apiRequest<SpaceList>('/api/v1/spaces', { signal });

export const getSpace = (id: string, signal?: AbortSignal) => apiRequest<Space>(`/api/v1/spaces/${id}`, { signal });

export const createSpace = (body: SpaceInput) => apiRequest<Space>('/api/v1/spaces', { method: 'POST', body });

export const updateSpace = (id: string, body: SpaceInput) =>
  apiRequest<Space>(`/api/v1/spaces/${id}`, { method: 'PATCH', body });

export const deleteSpace = (id: string) => apiRequest<void>(`/api/v1/spaces/${id}`, { method: 'DELETE' });

export const setSpaceGrant = (id: string, body: GrantInput) =>
  apiRequest<Space>(`/api/v1/spaces/${id}/grants`, { method: 'PUT', body });

export const getTree = (spaceId: string, signal?: AbortSignal) =>
  apiRequest<{ pages: PageNode[] }>(`/api/v1/spaces/${spaceId}/pages`, { signal }).then((r) => r.pages);

export const getTrash = (spaceId: string, signal?: AbortSignal) =>
  apiRequest<{ pages: TrashedPage[] }>(`/api/v1/spaces/${spaceId}/trash`, { signal }).then((r) => r.pages);

export const createPage = (
  spaceId: string,
  body: { parent_id?: string | null; title?: string; icon?: string; kind?: 'page' | 'database' }
) => apiRequest<Page>(`/api/v1/spaces/${spaceId}/pages`, { method: 'POST', body });

export const getPage = (id: string, signal?: AbortSignal) => apiRequest<Page>(`/api/v1/pages/${id}`, { signal });

export const updatePage = (id: string, body: { title?: string; icon?: string; is_template?: boolean }) =>
  apiRequest<Page>(`/api/v1/pages/${id}`, { method: 'PATCH', body });

/** Copies a page and its subtree ("new from template" and plain duplicate). */
export const duplicatePage = (id: string, body: { parent_id?: string | null; title?: string }) =>
  apiRequest<{ page: Page; warning?: string }>(`/api/v1/pages/${id}/duplicate`, { method: 'POST', body });

export const movePage = (id: string, body: { parent_id: string | null; position?: number }) =>
  apiRequest<Page>(`/api/v1/pages/${id}/move`, { method: 'POST', body });

export const trashPage = (id: string) => apiRequest<void>(`/api/v1/pages/${id}/trash`, { method: 'POST' });

export const restorePage = (id: string) => apiRequest<Page>(`/api/v1/pages/${id}/restore`, { method: 'POST' });

export const deletePage = (id: string) => apiRequest<void>(`/api/v1/pages/${id}`, { method: 'DELETE' });

/** The returned page has access 'none' when the change removed the caller's own access. */
export const setPageGrant = (id: string, body: GrantInput) =>
  apiRequest<Page>(`/api/v1/pages/${id}/grants`, { method: 'PUT', body });

export const setRestricted = (id: string, restricted: boolean) =>
  apiRequest<Page>(`/api/v1/pages/${id}/restricted`, { method: 'PUT', body: { restricted } });

/** A one-time ticket (30s) for opening the page's live editing websocket. */
export const getLiveTicket = (id: string) =>
  apiRequest<{ ticket: string; level: Level }>(`/api/v1/pages/${id}/live-ticket`, { method: 'POST' });

export type WikiFile = {
  id: string;
  page_id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
  created_at: string;
  /** Backend download path; serve it with fetchFileBlob (needs auth headers). */
  url: string;
};

export const uploadPageFile = (pageId: string, file: File) => {
  const form = new FormData();
  form.append('file', file, file.name);
  return apiUpload<WikiFile>(`/api/v1/pages/${pageId}/files`, form);
};

/** Download a file through the backend (the page access check runs there). */
export const fetchFileBlob = (url: string) => apiBlob(url);

export type Revision = {
  id: string;
  page_id: string;
  size_bytes: number;
  /** auto (periodic), restore (state replaced by a restore) or manual. */
  reason: 'auto' | 'restore' | 'manual';
  created_at: string;
};

export type RevisionList = {
  revisions: Revision[];
  /** Revisions older than this are deleted; 0 keeps every revision. */
  retention_days: number;
  /** den: set in Den's module settings; config: WIKI_REVISION_RETENTION_DAYS. */
  retention_source: 'den' | 'config';
};

export const listRevisions = (pageId: string, signal?: AbortSignal) =>
  apiRequest<RevisionList>(`/api/v1/pages/${pageId}/revisions`, { signal });

/** One revision's Yjs document, for the read-only preview. */
export const fetchRevisionDoc = (revisionId: string) =>
  apiBlob(`/api/v1/revisions/${revisionId}`)
    .then((b) => b.arrayBuffer())
    .then((buf) => new Uint8Array(buf));

/** Makes the revision the current document; open editors reload. */
export const restoreRevision = (revisionId: string) =>
  apiRequest<Page>(`/api/v1/revisions/${revisionId}/restore`, { method: 'POST' });

// --- Databases (ADR 0002) ---------------------------------------------------

export type PropertyType =
  | 'text'
  | 'number'
  | 'select'
  | 'multi_select'
  | 'date'
  | 'person'
  | 'checkbox'
  | 'url';

export type DBProperty = {
  id: string;
  database_id: string;
  name: string;
  type: PropertyType;
  options: { choices?: string[] } | null;
  position: number;
};

export type ViewConfig = {
  filter?: { property: string; equals: unknown }[];
  sort?: { property: string; desc: boolean }[];
  group_by?: string | null;
  date_property?: string | null;
};

export type DBView = {
  id: string;
  database_id: string;
  name: string;
  kind: 'table' | 'board' | 'calendar';
  config: ViewConfig;
  position: number;
};

export type DBRow = PageNode & { values: Record<string, unknown> };

export const getDatabase = (pageId: string, signal?: AbortSignal) =>
  apiRequest<{ properties: DBProperty[]; views: DBView[] }>(`/api/v1/pages/${pageId}/database`, { signal });

export const listRows = (pageId: string, viewId?: string, signal?: AbortSignal) =>
  apiRequest<{ rows: DBRow[] }>(`/api/v1/pages/${pageId}/rows`, { signal, query: { view: viewId } }).then(
    (r) => r.rows
  );

/** null removes a value. */
export const setRowValues = (pageId: string, values: Record<string, unknown>) =>
  apiRequest<DBRow>(`/api/v1/pages/${pageId}/values`, { method: 'PUT', body: { values } });

export const createProperty = (pageId: string, body: { name: string; type: PropertyType; options?: unknown }) =>
  apiRequest<DBProperty>(`/api/v1/pages/${pageId}/properties`, { method: 'POST', body });

export const updateProperty = (id: string, body: { name?: string; options?: unknown; position?: number }) =>
  apiRequest<DBProperty>(`/api/v1/properties/${id}`, { method: 'PATCH', body });

export const deleteProperty = (id: string) => apiRequest<void>(`/api/v1/properties/${id}`, { method: 'DELETE' });

export const createView = (pageId: string, body: { name: string; kind: DBView['kind']; config?: ViewConfig }) =>
  apiRequest<DBView>(`/api/v1/pages/${pageId}/views`, { method: 'POST', body });

export const updateView = (id: string, body: { name?: string; config?: ViewConfig; position?: number }) =>
  apiRequest<DBView>(`/api/v1/views/${id}`, { method: 'PATCH', body });

export const deleteView = (id: string) => apiRequest<void>(`/api/v1/views/${id}`, { method: 'DELETE' });

// --- Public share links -----------------------------------------------------

export type ShareLink = {
  id: string;
  page_id: string;
  include_children: boolean;
  created_at: string;
  expires_at: string | null;
  revoked_at: string | null;
  last_used_at: string | null;
};

export const listShareLinks = (pageId: string, signal?: AbortSignal) =>
  apiRequest<{ links: ShareLink[] }>(`/api/v1/pages/${pageId}/share-links`, { signal }).then((r) => r.links);

/** The token comes back exactly once; the share URL is /share/<token>. */
export const createShareLink = (pageId: string, body: { include_children: boolean; expires_in_days: number }) =>
  apiRequest<{ link: ShareLink; token: string }>(`/api/v1/pages/${pageId}/share-links`, { method: 'POST', body });

export const revokeShareLink = (linkId: string) =>
  apiRequest<void>(`/api/v1/share-links/${linkId}`, { method: 'DELETE' });

export type SharePage = { id: string; parent_id: string | null; title: string; icon: string };
export type ShareView = { page: SharePage; pages: SharePage[] };

/** Anonymous: no auth headers at all. */
const publicRequest = async <T>(path: string): Promise<T> => {
  const res = await fetch(apiURL(path).toString());
  if (!res.ok) {
    throw new ApiError(res.status, {
      error: { code: 'share_unavailable', message: 'This link is no longer available.' },
    });
  }
  return (await res.json()) as T;
};

export const getPublicShare = (token: string) => publicRequest<ShareView>(`/api/public/v1/shares/${token}`);

export const getPublicShareDoc = (token: string, pageId: string) =>
  publicRequest<{ updates: string[] }>(`/api/public/v1/shares/${token}/pages/${pageId}/doc`).then((r) =>
    r.updates.map((u) => Uint8Array.from(atob(u), (ch) => ch.charCodeAt(0)))
  );

export const publicShareFileURL = (token: string, fileId: string) =>
  `/api/public/v1/shares/${token}/files/${fileId}`;

// --- Comments -------------------------------------------------------------

export type RawReaction = { emoji: string; created_at: string; user_ids: string[] };

export type RawComment = {
  id: string;
  user_id: string | null;
  body: unknown;
  metadata: unknown;
  reactions: RawReaction[];
  created_at: string;
  updated_at: string;
  deleted_at: string | null;
};

export type RawThread = {
  id: string;
  page_id: string;
  resolved: boolean;
  resolved_by: string | null;
  resolved_at: string | null;
  metadata: unknown;
  comments: RawComment[];
  created_at: string;
  updated_at: string;
};

export const listThreads = (pageId: string, signal?: AbortSignal) =>
  apiRequest<{ threads: RawThread[] }>(`/api/v1/pages/${pageId}/threads`, { signal }).then((r) => r.threads);

export const createThread = (pageId: string, body: unknown, metadata?: unknown) =>
  apiRequest<RawThread>(`/api/v1/pages/${pageId}/threads`, { method: 'POST', body: { body, metadata } });

export const addThreadComment = (threadId: string, body: unknown) =>
  apiRequest<RawThread>(`/api/v1/threads/${threadId}/comments`, { method: 'POST', body: { body } });

export const updateThreadComment = (commentId: string, body: unknown) =>
  apiRequest<RawThread>(`/api/v1/comments/${commentId}`, { method: 'PATCH', body: { body } });

export const deleteThreadComment = (commentId: string) =>
  apiRequest<RawThread>(`/api/v1/comments/${commentId}`, { method: 'DELETE' });

export const deleteThread = (threadId: string) =>
  apiRequest<void>(`/api/v1/threads/${threadId}`, { method: 'DELETE' });

export const setThreadResolved = (threadId: string, resolved: boolean) =>
  apiRequest<RawThread>(`/api/v1/threads/${threadId}/${resolved ? 'resolve' : 'unresolve'}`, { method: 'POST' });

export const setCommentReaction = (commentId: string, emoji: string, add: boolean) =>
  apiRequest<RawThread>(`/api/v1/comments/${commentId}/reactions`, {
    method: add ? 'POST' : 'DELETE',
    body: { emoji },
  });

export const peopleByIds = (ids: string[], signal?: AbortSignal) =>
  ids.length === 0
    ? Promise.resolve([] as Person[])
    : apiRequest<{ people: Person[] }>('/api/v1/users', { signal, query: { ids: ids.join(',') } }).then(
        (r) => r.people
      );

export type SearchHit = {
  id: string;
  space_id: string;
  space_name: string;
  title: string;
  icon: string;
  /** Highlighted body-text fragment; '' for title-only matches and backlinks. */
  snippet: string;
};

export const searchPages = (q: string, signal?: AbortSignal) =>
  apiRequest<{ hits: SearchHit[] }>('/api/v1/search', { signal, query: { q } }).then((r) => r.hits);

export const getBacklinks = (pageId: string, signal?: AbortSignal) =>
  apiRequest<{ hits: SearchHit[] }>(`/api/v1/pages/${pageId}/backlinks`, { signal }).then((r) => r.hits);

export type Favorite = { page_id: string; space_id: string; title: string; icon: string };

export const listFavorites = (signal?: AbortSignal) =>
  apiRequest<{ favorites: Favorite[] }>('/api/v1/me/favorites', { signal }).then((r) => r.favorites);

export const addFavorite = (pageId: string) =>
  apiRequest<void>(`/api/v1/me/favorites/${pageId}`, { method: 'PUT' });

export const removeFavorite = (pageId: string) =>
  apiRequest<void>(`/api/v1/me/favorites/${pageId}`, { method: 'DELETE' });

export const searchPeople = (q: string, signal?: AbortSignal) =>
  apiRequest<{ people: Person[] }>('/api/v1/users', { signal, query: { q } }).then((r) => r.people);

/** department and access come from HR, idp from the sign-in server, token from the caller's own groups. */
export type DirectoryGroup = { name: string; display_name: string; kind: 'department' | 'access' | 'idp' | 'token' };

export type GroupList = {
  groups: DirectoryGroup[];
  source: 'hr' | 'idp' | 'token';
  /** Set when the directory failed and the list fell back to the caller's groups. */
  warning?: string;
};

export const listGroups = (signal?: AbortSignal) => apiRequest<GroupList>('/api/v1/groups', { signal });

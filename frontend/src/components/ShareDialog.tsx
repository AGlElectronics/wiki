import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { Copy, Globe, Link2, Lock, User, Users } from 'lucide-react';
import {
  atLeast,
  createShareLink,
  LEVEL_LABELS,
  listGroups,
  listShareLinks,
  revokeShareLink,
  searchPeople,
  setPageGrant,
  setRestricted,
  setSpaceGrant,
  updateSpace,
  type DirectoryGroup,
  type Grant,
  type GrantInput,
  type GrantLevel,
  type Page,
  type PrincipalKind,
  type Space,
} from '@/api/wiki';
import { Button } from '@/components/ui/Button';
import { Dialog, Select } from '@/components/ui/Dialog';
import { Input } from '@/components/ui/Input';
import { cn } from '@/lib/utils';

const GRANT_LEVELS = Object.keys(LEVEL_LABELS) as GrantLevel[];

type Target = { kind: 'space'; space: Space } | { kind: 'page'; page: Page };

/**
 * Shares a team space or a page with people and IdP groups. Page grants
 * add to what the page inherits; restricting a page stops inheritance.
 */
export function ShareDialog({ target, onClose }: { target: Target; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const grants = target.kind === 'space' ? target.space.grants : target.page.grants;
  const spaceId = target.kind === 'space' ? target.space.id : target.page.space_id;

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['spaces'] });
    void qc.invalidateQueries({ queryKey: ['space', spaceId] });
    void qc.invalidateQueries({ queryKey: ['tree', spaceId] });
    if (target.kind === 'page') void qc.invalidateQueries({ queryKey: ['page', target.page.id] });
  };
  const lostAccess = (access: string) => {
    if (access !== 'none') return false;
    onClose();
    void navigate({ to: '/' });
    return true;
  };

  const grant = useMutation({
    mutationFn: async (input: GrantInput) => {
      if (target.kind === 'space') return (await setSpaceGrant(target.space.id, input)).access;
      return (await setPageGrant(target.page.id, input)).access;
    },
    onSuccess: (access) => {
      refresh();
      lostAccess(access);
    },
  });
  const everyone = useMutation({
    mutationFn: (level: GrantLevel | '') => updateSpace(spaceId, { everyone_level: level }),
    onSuccess: refresh,
  });
  const restrict = useMutation({
    mutationFn: (restricted: boolean) => setRestricted(target.kind === 'page' ? target.page.id : '', restricted),
    onSuccess: refresh,
  });
  const error = grant.error ?? everyone.error ?? restrict.error;

  const title =
    target.kind === 'space'
      ? `Share ${target.space.name}`
      : `Share ${target.page.title.trim() || 'Untitled'}`;

  return (
    <Dialog title={title} onClose={onClose} className="max-w-lg">
      <AddGrant
        busy={grant.isPending}
        onAdd={(principal_kind, principal, level) => grant.mutate({ principal_kind, principal, level })}
      />

      {target.kind === 'page' ? (
        <div className="rounded-md border border-border p-3 space-y-2">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={target.page.restricted}
              disabled={restrict.isPending}
              onChange={(e) => restrict.mutate(e.target.checked)}
            />
            <span>
              <span className="font-semibold flex items-center gap-1.5">
                <Lock className="w-3.5 h-3.5" /> Only people listed here
              </span>
              <span className="block text-muted-foreground">
                {target.page.restricted
                  ? 'This page and its sub-pages ignore access from the space and parent pages.'
                  : `Everyone who can see ${target.page.breadcrumbs.at(-1)?.title || target.page.space_name} can see this page too.`}
              </span>
            </span>
          </label>
        </div>
      ) : (
        <div className="flex items-center gap-3 rounded-md border border-border p-3">
          <Globe className="w-4 h-4 text-muted-foreground shrink-0" />
          <div className="flex-1 text-sm">
            <div className="font-semibold">Everyone signed in</div>
            <div className="text-muted-foreground">Anyone in the organisation, without being invited.</div>
          </div>
          <Select
            value={target.space.everyone_level ?? ''}
            disabled={everyone.isPending}
            onChange={(e) => everyone.mutate(e.target.value as GrantLevel | '')}
            aria-label="Access for everyone signed in"
          >
            <option value="">No access</option>
            {GRANT_LEVELS.map((l) => (
              <option key={l} value={l}>
                {LEVEL_LABELS[l]}
              </option>
            ))}
          </Select>
        </div>
      )}

      <div className="space-y-1">
        <div className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
          {target.kind === 'page' ? 'Shared on this page' : 'People and groups'}
        </div>
        {grants.length === 0 ? (
          <p className="text-sm text-muted-foreground py-2">Nobody has been added yet.</p>
        ) : (
          grants.map((g) => (
            <GrantRow
              key={`${g.principal_kind}:${g.principal}`}
              grant={g}
              disabled={grant.isPending}
              onChange={(level) => grant.mutate({ principal_kind: g.principal_kind, principal: g.principal, level })}
            />
          ))
        )}
      </div>

      {error ? <p className="text-sm text-destructive">{(error as Error).message}</p> : null}

      {target.kind === 'page' && atLeast(target.page.access, 'full') ? (
        <PublicLinks pageId={target.page.id} />
      ) : null}
    </Dialog>
  );
}

/**
 * Public share links: read-only access for anyone with the URL. Off for
 * the whole organisation unless Den's wiki_public_links setting (or
 * PUBLIC_LINKS_ENABLED standalone) switches it on; links made while it is
 * off simply answer 404 until it is on.
 */
function PublicLinks({ pageId }: { pageId: string }) {
  const qc = useQueryClient();
  const [includeChildren, setIncludeChildren] = useState(true);
  const [expiresDays, setExpiresDays] = useState(0);
  const [freshURL, setFreshURL] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const links = useQuery({
    queryKey: ['share-links', pageId],
    queryFn: ({ signal }) => listShareLinks(pageId, signal),
  });
  const refresh = () => void qc.invalidateQueries({ queryKey: ['share-links', pageId] });
  const create = useMutation({
    mutationFn: () => createShareLink(pageId, { include_children: includeChildren, expires_in_days: expiresDays }),
    onSuccess: ({ token }) => {
      setFreshURL(`${window.location.origin}/share/${token}`);
      setCopied(false);
      refresh();
    },
  });
  const revoke = useMutation({ mutationFn: revokeShareLink, onSuccess: refresh });
  const active = (links.data ?? []).filter((l) => !l.revoked_at);

  return (
    <div className="rounded-md border border-border p-3 space-y-3">
      <div className="font-semibold text-sm flex items-center gap-1.5">
        <Link2 className="w-3.5 h-3.5" /> Public link
      </div>
      <p className="text-xs text-muted-foreground">
        Anyone with the link can read this page without an account. Works only while public links are switched on
        for the organisation (Den setting <code>wiki_public_links</code>).
      </p>
      {freshURL ? (
        <div className="flex items-center gap-2">
          <Input readOnly value={freshURL} className="flex-1 text-xs" aria-label="Share URL" />
          <Button
            type="button"
            size="sm"
            onClick={() => {
              void navigator.clipboard.writeText(freshURL).then(() => setCopied(true));
            }}
          >
            <Copy className="w-3.5 h-3.5" /> {copied ? 'Copied' : 'Copy'}
          </Button>
        </div>
      ) : null}
      {freshURL ? (
        <p className="text-xs text-warning">Copy it now — the link is shown only once.</p>
      ) : null}
      <div className="flex flex-wrap items-center gap-3 text-sm">
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={includeChildren}
            onChange={(e) => setIncludeChildren(e.target.checked)}
          />
          Include sub-pages
        </label>
        <Select
          value={String(expiresDays)}
          onChange={(e) => setExpiresDays(Number(e.target.value))}
          aria-label="Link expiry"
        >
          <option value="0">Never expires</option>
          <option value="7">Expires in 7 days</option>
          <option value="30">Expires in 30 days</option>
          <option value="90">Expires in 90 days</option>
        </Select>
        <Button type="button" size="sm" disabled={create.isPending} onClick={() => create.mutate()}>
          Create link
        </Button>
      </div>
      {create.isError ? <p className="text-sm text-destructive">{(create.error as Error).message}</p> : null}
      {active.length > 0 ? (
        <div className="space-y-1">
          {active.map((l) => (
            <div key={l.id} className="flex items-center gap-2 text-xs text-muted-foreground">
              <span className="flex-1 truncate">
                Created {new Date(l.created_at).toLocaleDateString()}
                {l.include_children ? ' · with sub-pages' : ''}
                {l.expires_at ? ` · expires ${new Date(l.expires_at).toLocaleDateString()}` : ''}
                {l.last_used_at ? ` · last used ${new Date(l.last_used_at).toLocaleDateString()}` : ' · never used'}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={revoke.isPending}
                onClick={() => revoke.mutate(l.id)}
              >
                Revoke
              </Button>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function GrantRow({
  grant,
  disabled,
  onChange,
}: {
  grant: Grant;
  disabled: boolean;
  onChange: (level: GrantLevel | '') => void;
}) {
  const Icon = grant.principal_kind === 'group' ? Users : User;
  return (
    <div className="flex items-center gap-3 py-1.5">
      <span className="rounded-pill bg-secondary p-1.5">
        <Icon className="w-4 h-4" />
      </span>
      <span className="flex-1 text-sm truncate">
        {grant.display_name || grant.principal}
        {grant.principal_kind === 'group' ? <span className="text-muted-foreground"> · group</span> : null}
      </span>
      <Select
        value={grant.level}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value === 'remove' ? '' : (e.target.value as GrantLevel))}
        aria-label={`Access for ${grant.display_name || grant.principal}`}
      >
        {GRANT_LEVELS.map((l) => (
          <option key={l} value={l}>
            {LEVEL_LABELS[l]}
          </option>
        ))}
        <option value="remove">Remove</option>
      </Select>
    </div>
  );
}

function AddGrant({
  busy,
  onAdd,
}: {
  busy: boolean;
  onAdd: (kind: PrincipalKind, principal: string, level: GrantLevel) => void;
}) {
  const [kind, setKind] = useState<PrincipalKind>('user');
  const [query, setQuery] = useState('');
  const [picked, setPicked] = useState<{ id: string; label: string } | null>(null);
  const [level, setLevel] = useState<GrantLevel>('view');

  const people = useQuery({
    queryKey: ['people', query],
    queryFn: ({ signal }) => searchPeople(query, signal),
    enabled: kind === 'user' && query.trim().length > 0 && !picked,
  });
  const groups = useQuery({
    queryKey: ['groups'],
    queryFn: ({ signal }) => listGroups(signal),
    enabled: kind === 'group',
  });

  const q = query.trim().toLowerCase();
  const suggestions =
    picked || !q
      ? []
      : kind === 'user'
        ? (people.data ?? []).map((p) => ({ id: p.id, label: p.display_name, hint: p.email ?? '' }))
        : (groups.data?.groups ?? [])
            .filter((g) => g.name.toLowerCase().includes(q) || g.display_name.toLowerCase().includes(q))
            .map((g) => ({ id: g.name, label: g.display_name, hint: groupHint(g) }));

  // Any group name can be typed in, also one the directory does not list.
  const principal = picked?.id ?? (kind === 'group' ? query.trim() : '');

  const reset = () => {
    setQuery('');
    setPicked(null);
  };

  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (!principal) return;
        onAdd(kind, principal, level);
        reset();
      }}
    >
      <div className="flex gap-1 text-sm">
        {(['user', 'group'] as const).map((k) => (
          <button
            key={k}
            type="button"
            className={cn(
              'px-3 py-1 rounded-pill font-medium transition-colors',
              kind === k ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-secondary'
            )}
            onClick={() => {
              setKind(k);
              reset();
            }}
          >
            {k === 'user' ? 'Person' : 'Group'}
          </button>
        ))}
      </div>
      <div className="flex gap-2">
        <div className="relative flex-1">
          <Input
            value={picked?.label ?? query}
            placeholder={kind === 'user' ? 'Search people by name or email' : 'Group name, e.g. dept-security'}
            onChange={(e) => {
              setPicked(null);
              setQuery(e.target.value);
            }}
            autoFocus
          />
          {suggestions.length > 0 ? (
            <div className="absolute z-10 mt-1 w-full max-h-56 overflow-y-auto rounded-md border border-border bg-card shadow-pop">
              {suggestions.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  className="w-full text-left px-3 py-2 text-sm hover:bg-secondary"
                  onClick={() => setPicked({ id: s.id, label: kind === 'group' ? s.id : s.label })}
                >
                  {s.label}
                  {s.hint ? <span className="block text-xs text-muted-foreground">{s.hint}</span> : null}
                </button>
              ))}
            </div>
          ) : null}
        </div>
        <Select value={level} onChange={(e) => setLevel(e.target.value as GrantLevel)} aria-label="Access level">
          {GRANT_LEVELS.map((l) => (
            <option key={l} value={l}>
              {LEVEL_LABELS[l]}
            </option>
          ))}
        </Select>
        <Button type="submit" disabled={!principal || busy}>
          Invite
        </Button>
      </div>
      {kind === 'group' && groups.data?.warning ? (
        <p className="text-xs text-warning">{groups.data.warning}</p>
      ) : null}
    </form>
  );
}

const GROUP_KIND_LABELS: Record<DirectoryGroup['kind'], string> = {
  department: 'Department',
  access: 'Access group',
  idp: 'Sign-in group',
  token: 'Your group',
};

function groupHint(g: DirectoryGroup) {
  const kind = GROUP_KIND_LABELS[g.kind] ?? '';
  return g.display_name === g.name ? kind : `${kind} · ${g.name}`;
}

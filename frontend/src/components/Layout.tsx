import { useEffect, useState } from 'react';
import { Link, Outlet, useRouterState } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import { BookOpen, ChevronRight, House, LogOut, Menu, Plus, Search, Settings, X } from 'lucide-react';
import { atLeast, listFavorites, listSpaces, type Space } from '@/api/wiki';
import { isOidcAuthMode, signOutOidc } from '@/auth/oidc';
import { dropTarget, PageTree, useCreatePage, useExpanded, useMovePage } from '@/components/PageTree';
import { SearchPalette } from '@/components/SearchPalette';
import { SpaceDialog } from '@/components/SpaceDialog';
import { ThemeToggle } from '@/components/ThemeToggle';
import { useBrand } from '@/brand/BrandProvider';
import { useMe, PERMS } from '@/hooks/useMe';
import { useThemeSync } from '@/lib/theme';
import { cn } from '@/lib/utils';

type NavItem = {
  to: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  perms: string[]; // empty = always visible
};

const NAV: NavItem[] = [
  { to: '/', label: 'Home', icon: House, perms: [] },
  { to: '/settings', label: 'Settings', icon: Settings, perms: [PERMS.settingsManage] },
];

function Wordmark() {
  const { brand } = useBrand();
  return (
    <div className="flex items-center gap-2.5">
      {brand.logoUrl ? (
        <img src={brand.logoUrl} alt="" className="h-8 w-8 rounded-md object-contain" />
      ) : (
        <span className="rounded-pill bg-primary-soft p-1.5">
          <BookOpen className="w-5 h-5 text-primary" />
        </span>
      )}
      <span className="leading-none">
        <span className="block font-heading text-xl font-extrabold tracking-tight">
          {brand.productName}
        </span>
        {brand.organizationName ? (
          <span className="block text-[11px] font-medium text-muted-foreground mt-0.5">
            {brand.organizationName}
          </span>
        ) : null}
      </span>
    </div>
  );
}

function NavLinks({ onNavigate, onSearch }: { onNavigate?: () => void; onSearch: () => void }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { canAny } = useMe();
  return (
    <div className="space-y-0.5">
      <button
        type="button"
        className="w-full flex items-center gap-2.5 px-3 py-2 rounded-pill text-sm font-medium text-muted-foreground hover:bg-secondary hover:text-foreground transition-colors"
        onClick={onSearch}
      >
        <Search className="w-4 h-4 shrink-0" />
        <span className="flex-1 text-left">Search</span>
        <kbd className="text-[10px] border border-border rounded px-1">Ctrl K</kbd>
      </button>
      {NAV.map((item) => {
        const visible = item.perms.length === 0 || canAny(...item.perms);
        if (!visible) return null;
        const isActive =
          pathname === item.to || (item.to !== '/' && pathname.startsWith(item.to + '/'));
        return (
          <Link
            key={`${item.to}-${item.label}`}
            to={item.to}
            onClick={onNavigate}
            className={cn(
              'flex items-center gap-2.5 px-3 py-2 rounded-pill text-sm font-medium whitespace-nowrap transition-colors',
              isActive
                ? 'bg-accent text-accent-foreground'
                : 'text-muted-foreground hover:bg-secondary hover:text-foreground'
            )}
          >
            <item.icon className="w-4 h-4 shrink-0" />
            {item.label}
          </Link>
        );
      })}
      <WikiNav onNavigate={onNavigate} />
    </div>
  );
}

function SectionTitle({ children, action }: { children: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between px-3 pt-4 pb-1">
      <span className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">{children}</span>
      {action}
    </div>
  );
}

function SpaceSection({ space, onNavigate }: { space: Space; onNavigate?: () => void }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const [open, setOpen] = useExpanded(`space:${space.id}`);
  const [over, setOver] = useState(false);
  const create = useCreatePage(space.id);
  const move = useMovePage(space.id);
  // Private spaces are always open: they are the user's own scratchpad.
  const shown = space.kind === 'private' || open;
  return (
    <div className="pb-0.5">
      <div
        className={cn(
          'group flex items-center gap-1 pl-2 pr-1 rounded-md text-sm font-semibold transition-colors',
          pathname === `/s/${space.id}` ? 'bg-accent text-accent-foreground' : 'hover:bg-secondary',
          over && 'ring-2 ring-primary'
        )}
        {...dropTarget(space.id, null, move, setOver)}
      >
        {space.kind === 'team' ? (
          <button
            type="button"
            className="p-0.5 rounded hover:bg-background/60"
            aria-label={open ? 'Collapse' : 'Expand'}
            onClick={() => setOpen(!open)}
          >
            <ChevronRight className={cn('w-3.5 h-3.5 transition-transform', open && 'rotate-90')} />
          </button>
        ) : null}
        <Link
          to="/s/$spaceId"
          params={{ spaceId: space.id }}
          onClick={onNavigate}
          className="flex-1 min-w-0 flex items-center gap-1.5 py-1.5"
        >
          <span className="w-4 text-center shrink-0">{space.icon || '📚'}</span>
          <span className="truncate">{space.name}</span>
        </Link>
        {atLeast(space.access, 'edit') ? (
          <button
            type="button"
            className="p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-background/60 focus-visible:opacity-100"
            aria-label={`Add a page to ${space.name}`}
            disabled={create.isPending}
            onClick={() => {
              setOpen(true);
              create.mutate(null);
            }}
          >
            <Plus className="w-3.5 h-3.5" />
          </button>
        ) : null}
      </div>
      {shown ? <PageTree spaceId={space.id} onNavigate={onNavigate} emptyHint="No pages yet" /> : null}
    </div>
  );
}

function Favorites({ onNavigate }: { onNavigate?: () => void }) {
  const favorites = useQuery({ queryKey: ['favorites'], queryFn: ({ signal }) => listFavorites(signal) });
  if (!favorites.data || favorites.data.length === 0) return null;
  return (
    <>
      <SectionTitle>Favourites</SectionTitle>
      {favorites.data.map((f) => (
        <Link
          key={f.page_id}
          to="/p/$pageId"
          params={{ pageId: f.page_id }}
          onClick={onNavigate}
          className="flex items-center gap-1.5 pl-3 pr-2 py-1.5 rounded-md text-sm hover:bg-secondary"
        >
          <span className="w-4 text-center shrink-0">{f.icon || '📄'}</span>
          <span className="truncate">{f.title.trim() || 'Untitled'}</span>
        </Link>
      ))}
    </>
  );
}

function WikiNav({ onNavigate }: { onNavigate?: () => void }) {
  const [creating, setCreating] = useState(false);
  const spaces = useQuery({ queryKey: ['spaces'], queryFn: ({ signal }) => listSpaces(signal) });
  if (!spaces.data) return null;
  const personal = spaces.data.spaces.filter((s) => s.kind === 'private' && !s.orphaned_at);
  const orphaned = spaces.data.spaces.filter((s) => s.kind === 'private' && s.orphaned_at);
  const team = spaces.data.spaces.filter((s) => s.kind === 'team');
  const sharedSpaces = [...new Map(spaces.data.shared.map((p) => [p.space_id, p.space_name])).entries()];
  return (
    <div>
      <Favorites onNavigate={onNavigate} />
      {personal.map((s) => (
        <SpaceSection key={s.id} space={s} onNavigate={onNavigate} />
      ))}
      <SectionTitle
        action={
          spaces.data.can_create ? (
            <button
              type="button"
              className="p-0.5 rounded text-muted-foreground hover:bg-secondary hover:text-foreground"
              aria-label="New team space"
              onClick={() => setCreating(true)}
            >
              <Plus className="w-3.5 h-3.5" />
            </button>
          ) : null
        }
      >
        Team spaces
      </SectionTitle>
      {team.length === 0 ? (
        <div className="px-3 py-1 text-xs text-muted-foreground">No team spaces shared with you yet.</div>
      ) : (
        team.map((s) => <SpaceSection key={s.id} space={s} onNavigate={onNavigate} />)
      )}
      {orphaned.length > 0 ? (
        <>
          <SectionTitle>Offboarded</SectionTitle>
          {orphaned.map((s) => (
            <SpaceSection key={s.id} space={s} onNavigate={onNavigate} />
          ))}
        </>
      ) : null}
      {sharedSpaces.length > 0 ? (
        <>
          <SectionTitle>Shared with you</SectionTitle>
          {sharedSpaces.map(([id, name]) => (
            <div key={id} className="pb-1">
              <div className="px-3 py-0.5 text-xs text-muted-foreground truncate">{name}</div>
              <PageTree spaceId={id} onNavigate={onNavigate} />
            </div>
          ))}
        </>
      ) : null}
      {creating ? <SpaceDialog onClose={() => setCreating(false)} /> : null}
    </div>
  );
}

export function Layout() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [searching, setSearching] = useState(false);
  useThemeSync('/api/v1/me/preferences');

  // Collapse the mobile drawer whenever the route changes.
  useEffect(() => {
    setDrawerOpen(false);
  }, [pathname]);

  // Ctrl+K / Cmd+K opens search from anywhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setSearching((s) => !s);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const footer = (
    <div className="mt-auto p-2 border-t border-border space-y-0.5">
      <ThemeToggle className="rounded-pill" />
      {isOidcAuthMode() ? (
        <button
          type="button"
          className="w-full flex items-center gap-2 text-left text-sm px-3 py-2 rounded-pill text-muted-foreground hover:bg-secondary hover:text-foreground transition-colors"
          onClick={() => {
            signOutOidc().catch(() => undefined);
          }}
        >
          <LogOut className="w-4 h-4 shrink-0" />
          Sign out
        </button>
      ) : null}
    </div>
  );

  return (
    <div className="min-h-screen w-full flex flex-col md:flex-row bg-background text-foreground">
      <header className="md:hidden sticky top-0 z-40 flex items-center gap-2 px-3 py-2.5 border-b border-border bg-card">
        <button
          type="button"
          className="p-2 rounded-pill hover:bg-secondary transition-colors"
          onClick={() => setDrawerOpen(true)}
          aria-label="Open menu"
        >
          <Menu className="w-5 h-5" />
        </button>
        <Wordmark />
      </header>

      {drawerOpen ? (
        <div
          className="md:hidden fixed inset-0 z-50 bg-black/50 backdrop-blur-[2px]"
          onClick={() => setDrawerOpen(false)}
        >
          <aside
            className="absolute inset-y-0 left-0 w-72 max-w-[85vw] bg-card border-r border-border flex flex-col"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="px-4 py-4 flex items-center justify-between border-b border-border">
              <Wordmark />
              <button
                type="button"
                className="p-2 rounded-pill hover:bg-secondary transition-colors"
                onClick={() => setDrawerOpen(false)}
                aria-label="Close menu"
              >
                <X className="w-5 h-5" />
              </button>
            </div>
            <nav className="p-2 flex-1 overflow-y-auto">
              <NavLinks
                onNavigate={() => setDrawerOpen(false)}
                onSearch={() => {
                  setDrawerOpen(false);
                  setSearching(true);
                }}
              />
            </nav>
            {footer}
          </aside>
        </div>
      ) : null}

      <aside className="hidden md:flex shrink-0 md:w-72 md:h-screen md:sticky md:top-0 border-r border-border bg-card flex-col">
        <div className="px-4 py-4 border-b border-border">
          <Wordmark />
        </div>
        <nav className="p-2 md:flex-1 overflow-y-auto">
          <NavLinks onSearch={() => setSearching(true)} />
        </nav>
        {footer}
      </aside>

      <main className="flex-1 min-w-0 min-h-0 flex flex-col w-full md:min-h-screen">
        <Outlet />
      </main>

      {searching ? <SearchPalette onClose={() => setSearching(false)} /> : null}
    </div>
  );
}

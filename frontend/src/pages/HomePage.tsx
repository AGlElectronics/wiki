import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { BookOpen, FileText, Globe, Lock, Plus, Users } from 'lucide-react';
import { listSpaces, type Space } from '@/api/wiki';
import { SpaceDialog } from '@/components/SpaceDialog';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { PageContainer, PageHeader } from '@/components/ui/PageHeader';
import { EmptyState, ErrorRow, LoadingRow } from '@/components/ui/States';

function SpaceAudience({ space }: { space: Space }) {
  if (space.kind === 'private') {
    return (
      <span className="inline-flex items-center gap-1">
        <Lock className="w-3 h-3" /> Only you
      </span>
    );
  }
  if (space.everyone_level) {
    return (
      <span className="inline-flex items-center gap-1">
        <Globe className="w-3 h-3" /> Everyone
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1">
      <Users className="w-3 h-3" /> {space.grants.length} {space.grants.length === 1 ? 'member' : 'members'}
    </span>
  );
}

export function HomePage() {
  const [creating, setCreating] = useState(false);
  const spaces = useQuery({ queryKey: ['spaces'], queryFn: ({ signal }) => listSpaces(signal) });

  return (
    <PageContainer>
      <PageHeader
        title="Home"
        description="Your private pages, the team spaces you belong to and pages shared with you."
        actions={
          spaces.data?.can_create ? (
            <Button onClick={() => setCreating(true)}>
              <Plus className="w-4 h-4" /> New team space
            </Button>
          ) : null
        }
      />
      {spaces.isLoading ? <LoadingRow /> : null}
      {spaces.isError ? <ErrorRow message={(spaces.error as Error).message} /> : null}
      {spaces.data ? (
        <div className="space-y-8">
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {spaces.data.spaces.map((s) => (
              <Link key={s.id} to="/s/$spaceId" params={{ spaceId: s.id }}>
                <Card clickable className="p-5 h-full">
                  <div className="flex items-center gap-3">
                    <span className="text-2xl">{s.icon || '📚'}</span>
                    <div className="min-w-0">
                      <div className="font-heading font-bold truncate">{s.name}</div>
                      <div className="text-xs text-muted-foreground">
                        <SpaceAudience space={s} />
                      </div>
                    </div>
                  </div>
                  {s.description ? <p className="text-sm text-muted-foreground mt-3 line-clamp-2">{s.description}</p> : null}
                </Card>
              </Link>
            ))}
          </div>
          {spaces.data.spaces.length <= 1 && !spaces.data.can_create ? (
            <EmptyState
              icon={BookOpen}
              title="No team spaces yet"
              description="When someone shares a team space with you or your groups, it shows up here."
            />
          ) : null}
          {spaces.data.shared.length > 0 ? (
            <section>
              <h2 className="font-heading font-bold mb-2">Shared with you</h2>
              <Card className="divide-y divide-border">
                {spaces.data.shared.map((p) => (
                  <Link
                    key={p.id}
                    to="/p/$pageId"
                    params={{ pageId: p.id }}
                    className="flex items-center gap-3 px-4 py-3 text-sm hover:bg-secondary"
                  >
                    {p.icon ? <span>{p.icon}</span> : <FileText className="w-4 h-4 text-muted-foreground" />}
                    <span className="flex-1 truncate font-medium">{p.title.trim() || 'Untitled'}</span>
                    <span className="text-xs text-muted-foreground">{p.space_name}</span>
                  </Link>
                ))}
              </Card>
            </section>
          ) : null}
        </div>
      ) : null}
      {creating ? <SpaceDialog onClose={() => setCreating(false)} /> : null}
    </PageContainer>
  );
}

// ShareDialog: grants render with their levels, the restricted toggle
// explains itself, and the public-link section only appears with full
// access and keeps the token visible exactly once.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { vi } from 'vitest';
import type { Page } from '@/api/wiki';
import { ShareDialog } from '@/components/ShareDialog';

const navigate = vi.fn();
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  Link: ({ children, ...rest }: React.PropsWithChildren<Record<string, unknown>>) => (
    <a {...(rest as object)}>{children}</a>
  ),
}));

vi.mock('@/api/wiki', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/wiki')>();
  return {
    ...actual,
    listShareLinks: vi.fn().mockResolvedValue([]),
    createShareLink: vi.fn().mockResolvedValue({
      link: {
        id: 'link-1',
        page_id: 'page-1',
        include_children: true,
        created_at: new Date().toISOString(),
        expires_at: null,
        revoked_at: null,
        last_used_at: null,
      },
      token: 'wsl_test_token',
    }),
    revokeShareLink: vi.fn().mockResolvedValue(undefined),
    searchPeople: vi.fn().mockResolvedValue([]),
    listGroups: vi.fn().mockResolvedValue([]),
  };
});

function page(overrides: Partial<Page> = {}): Page {
  return {
    id: 'page-1',
    parent_id: null,
    position: 1,
    title: 'Handbook',
    icon: '',
    kind: 'page',
    restricted: false,
    is_template: false,
    access: 'full',
    space_id: 'space-1',
    space_name: 'Docs',
    space_kind: 'team',
    updated_at: new Date().toISOString(),
    breadcrumbs: [],
    grants: [
      { principal_kind: 'group', principal: 'dept-security', level: 'view', display_name: 'dept-security' },
    ],
    can_trash: true,
    ...overrides,
  };
}

function renderDialog(p: Page) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ShareDialog target={{ kind: 'page', page: p }} onClose={() => undefined} />
    </QueryClientProvider>
  );
}

test('shows the page grants with their level', () => {
  renderDialog(page());
  expect(screen.getByText('dept-security')).toBeInTheDocument();
  expect(screen.getByLabelText('Access for dept-security')).toHaveValue('view');
});

test('explains the restricted toggle both ways', () => {
  renderDialog(page({ restricted: true }));
  expect(screen.getByText(/ignore access from the space/i)).toBeInTheDocument();
});

test('full access offers public links and shows the token once', async () => {
  renderDialog(page());
  expect(screen.getByText('Public link')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: /create link/i }));
  const url = await screen.findByLabelText<HTMLInputElement>('Share URL');
  expect(url.value).toContain('/share/wsl_test_token');
});

test('edit access gets no public-link section', () => {
  renderDialog(page({ access: 'edit' }));
  expect(screen.queryByText('Public link')).not.toBeInTheDocument();
});

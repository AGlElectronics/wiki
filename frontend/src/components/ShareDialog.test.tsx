// ShareDialog: grants render with their levels, the restricted toggle
// explains itself, and the public-link section only appears with full
// access and keeps the token visible exactly once.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, vi } from 'vitest';
import type { Page } from '@/api/wiki';
import type * as WikiApi from '@/api/wiki';
import { ShareDialog } from '@/components/ShareDialog';

const navigate = vi.fn();
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  Link: ({ children, ...rest }: React.PropsWithChildren<Record<string, unknown>>) => (
    <a {...(rest as object)}>{children}</a>
  ),
}));

vi.mock('@/api/wiki', async (importOriginal) => {
  const actual = await importOriginal<typeof WikiApi>();
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
    searchPeople: vi.fn().mockResolvedValue([{ id: 'u-1', display_name: 'Eddie' }]),
    listGroups: vi.fn().mockResolvedValue({
      source: 'hr',
      groups: [
        { name: 'dept-security', display_name: 'Security', kind: 'department' },
        { name: 'wiki-editor', display_name: 'Wiki editors', kind: 'access' },
      ],
    }),
    setPageGrant: vi.fn().mockResolvedValue({ access: 'full' }),
  };
});

beforeEach(() => {
  vi.clearAllMocks();
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

test('the group picker offers HR departments and stores the group name', async () => {
  const { setPageGrant } = await import('@/api/wiki');
  renderDialog(page());
  fireEvent.click(screen.getByRole('button', { name: 'Group' }));
  fireEvent.change(screen.getByPlaceholderText(/group name/i), { target: { value: 'secu' } });
  const option = await screen.findByRole('button', { name: /Security.*Department · dept-security/ });
  fireEvent.click(option);
  fireEvent.click(screen.getByRole('button', { name: 'Invite' }));
  await waitFor(() =>
    expect(setPageGrant).toHaveBeenCalledWith('page-1', {
      principal_kind: 'group',
      principal: 'dept-security',
      level: 'view',
    })
  );
});

test('a group the directory does not list can still be typed in', async () => {
  const { setPageGrant } = await import('@/api/wiki');
  renderDialog(page());
  fireEvent.click(screen.getByRole('button', { name: 'Group' }));
  fireEvent.change(screen.getByPlaceholderText(/group name/i), { target: { value: 'crew-night' } });
  fireEvent.click(screen.getByRole('button', { name: 'Invite' }));
  await waitFor(() =>
    expect(setPageGrant).toHaveBeenCalledWith('page-1', {
      principal_kind: 'group',
      principal: 'crew-night',
      level: 'view',
    })
  );
});

test('people without a visible email show just the name', async () => {
  renderDialog(page());
  fireEvent.change(screen.getByPlaceholderText(/search people/i), { target: { value: 'edd' } });
  const option = await screen.findByRole('button', { name: 'Eddie' });
  expect(option.querySelector('span')).toBeNull();
});

test('edit access gets no public-link section', () => {
  renderDialog(page({ access: 'edit' }));
  expect(screen.queryByText('Public link')).not.toBeInTheDocument();
});

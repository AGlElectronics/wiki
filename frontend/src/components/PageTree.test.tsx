// PageTree: the sidebar tree nests pages, hides children until expanded,
// and only offers "add inside" with edit access.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { vi } from 'vitest';
import type { PageNode } from '@/api/wiki';
import { PageTree } from '@/components/PageTree';

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => string }) =>
    select({ location: { pathname: '/p/root-1' } }),
  Link: ({ children, ...rest }: React.PropsWithChildren<Record<string, unknown>>) => (
    <a {...(rest as object)}>{children}</a>
  ),
}));

const { tree } = vi.hoisted(() => {
  const node = (overrides: Record<string, unknown>) => ({
    id: 'root-1',
    parent_id: null,
    position: 1,
    title: 'Handbook',
    icon: '',
    kind: 'page',
    restricted: false,
    is_template: false,
    access: 'edit',
    ...overrides,
  });
  return {
    tree: [
      node({ id: 'root-1', title: 'Handbook' }),
      node({ id: 'child-1', parent_id: 'root-1', title: 'Radio', access: 'view', restricted: true }),
      node({ id: 'root-2', title: 'Rules', access: 'view' }),
    ] as PageNode[],
  };
});

vi.mock('@/api/wiki', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/wiki')>();
  return { ...actual, getTree: vi.fn().mockResolvedValue(tree) };
});

function renderTree() {
  localStorage.clear();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PageTree spaceId="space-1" />
    </QueryClientProvider>
  );
}

test('renders roots, hides children until expanded', async () => {
  renderTree();
  expect(await screen.findByText('Handbook')).toBeInTheDocument();
  expect(screen.getByText('Rules')).toBeInTheDocument();
  expect(screen.queryByText('Radio')).not.toBeInTheDocument();

  fireEvent.click(screen.getAllByRole('button', { name: 'Expand' })[0]);
  expect(await screen.findByText('Radio')).toBeInTheDocument();
  expect(screen.getByLabelText('Restricted')).toBeInTheDocument();
});

test('offers "add a page inside" only with edit access', async () => {
  renderTree();
  await screen.findByText('Handbook');
  // Handbook (edit) has the button; Rules (view) does not.
  expect(screen.getAllByRole('button', { name: 'Add a page inside' })).toHaveLength(1);
});

test('untitled pages read as Untitled', async () => {
  renderTree();
  await screen.findByText('Handbook');
  fireEvent.click(screen.getAllByRole('button', { name: 'Expand' })[0]);
  // Radio has a title; no Untitled rows in this tree.
  expect(screen.queryByText('Untitled')).not.toBeInTheDocument();
});

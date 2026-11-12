// PageDiscussions: page-level threads only, a composer from comment level
// up that starts unanchored threads, and editor-only deletion.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, vi } from 'vitest';
import type { Level, RawThread } from '@/api/wiki';
import type * as WikiApi from '@/api/wiki';
import { PageDiscussions } from '@/components/PageDiscussions';
import { bodyToText, textToBody } from '@/lib/comments';

function thread(id: string, anchored: boolean, text: string): RawThread {
  const now = new Date().toISOString();
  return {
    id,
    page_id: 'page-1',
    anchored,
    resolved: false,
    resolved_by: null,
    resolved_at: null,
    metadata: null,
    created_at: now,
    updated_at: now,
    comments: [
      { id: `${id}-c`, user_id: 'u-1', body: textToBody(text), metadata: null, reactions: [], created_at: now, updated_at: now, deleted_at: null },
    ],
  };
}

vi.mock('@/api/wiki', async (importOriginal) => {
  const actual = await importOriginal<typeof WikiApi>();
  return {
    ...actual,
    listThreads: vi.fn(),
    createThread: vi.fn(),
    addThreadComment: vi.fn(),
    deleteThread: vi.fn(),
    setThreadResolved: vi.fn(),
    peopleByIds: vi.fn().mockResolvedValue([{ id: 'u-1', display_name: 'Sam' }]),
  };
});

const api = await import('@/api/wiki');

beforeEach(() => {
  vi.mocked(api.listThreads).mockResolvedValue([
    thread('t-page', false, 'Is this still current?'),
    thread('t-inline', true, 'Inline note on a sentence'),
  ]);
  vi.mocked(api.createThread).mockResolvedValue(thread('t-new', false, 'new'));
  vi.mocked(api.createThread).mockClear();
});

function renderPanel(level: Level) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PageDiscussions pageId="page-1" level={level} />
    </QueryClientProvider>
  );
}

test('shows page-level discussions, not inline threads', async () => {
  renderPanel('view');
  expect(await screen.findByText('Is this still current?')).toBeInTheDocument();
  expect(screen.queryByText('Inline note on a sentence')).not.toBeInTheDocument();
  expect(await screen.findByText('Sam')).toBeInTheDocument();
});

test('view level reads but cannot write', async () => {
  renderPanel('view');
  await screen.findByText('Is this still current?');
  expect(screen.queryByLabelText('New discussion')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Reply')).not.toBeInTheDocument();
});

test('comment level starts an unanchored discussion and is told why', async () => {
  renderPanel('comment');
  await screen.findByText('Is this still current?');
  expect(screen.getByText(/comments on a piece of text need edit access/i)).toBeInTheDocument();
  expect(screen.queryByLabelText('Delete discussion')).not.toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('New discussion'), { target: { value: 'Line one\nLine two' } });
  fireEvent.click(screen.getByRole('button', { name: 'Comment' }));
  await waitFor(() =>
    expect(api.createThread).toHaveBeenCalledWith('page-1', textToBody('Line one\nLine two'), undefined, false)
  );
});

test('edit level may delete a discussion', async () => {
  renderPanel('edit');
  await screen.findByText('Is this still current?');
  expect(screen.getByLabelText('Delete discussion')).toBeInTheDocument();
});

test('comment bodies round-trip as text', () => {
  expect(bodyToText(textToBody('a\nb'))).toBe('a\nb');
  expect(bodyToText([{ type: 'paragraph', content: 'plain' }])).toBe('plain');
  expect(bodyToText(null)).toBe('');
});

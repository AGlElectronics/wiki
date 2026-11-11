// DenConnection: a revoked credential asks the admin to pair again by hand.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, vi } from 'vitest';
import { DenConnection } from '@/components/DenConnection';

const apiRequest = vi.fn();
vi.mock('@/api/client', () => ({ apiRequest: (...args: unknown[]) => apiRequest(...args) }));

function renderIt() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DenConnection base="/api/v1/den" />
    </QueryClientProvider>
  );
}

beforeEach(() => apiRequest.mockReset());

test('a revoked credential shows the pair-again form', async () => {
  apiRequest.mockResolvedValue({
    paired: true,
    den_base_url: 'https://den.example.org',
    revoked: true,
    revoked_at: '2026-11-10T22:42:00Z',
    last_error: 'Den rejected this module',
  });
  renderIt();
  expect(await screen.findByRole('alert')).toHaveTextContent(/Den rejected this module's credential/);
  expect(screen.getByText(/Pair again manually/)).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /sync now/i })).not.toBeInTheDocument();

  fireEvent.change(screen.getByLabelText('Pairing URL'), { target: { value: 'https://den.example.org/redeem?token=x' } });
  fireEvent.click(screen.getByRole('button', { name: 'Pair again' }));
  await waitFor(() =>
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/den/pair', {
      method: 'POST',
      body: { pairing_url: 'https://den.example.org/redeem?token=x' },
    })
  );
});

test('a healthy pairing offers sync, not the pairing form', async () => {
  apiRequest.mockResolvedValue({ paired: true, den_base_url: 'https://den.example.org', revoked: false });
  renderIt();
  expect(await screen.findByRole('button', { name: /sync now/i })).toBeInTheDocument();
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Pairing URL')).not.toBeInTheDocument();
});

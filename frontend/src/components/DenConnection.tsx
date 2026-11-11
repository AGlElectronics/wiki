import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiRequest } from '@/api/client';

type DenStatus = {
  paired: boolean;
  den_base_url?: string;
  paired_at?: string;
  synced_at?: string;
  last_error?: string;
  /** Den rejected the stored credential (revoked, or the module was removed in Den). */
  revoked?: boolean;
  revoked_at?: string;
};

/**
 * Pairing with Den. Paired: brand and organisation settings come from Den.
 * Standalone: this module's own settings apply. Revoked: Den rejected the
 * credential; the last brand and settings stay until an admin pairs again
 * (never automatically).
 */
export function DenConnection({ base, onPaired }: { base: string; onPaired?: () => void }) {
  const qc = useQueryClient();
  const status = useQuery({ queryKey: ['den-status'], queryFn: () => apiRequest<DenStatus>(`${base}/`) });
  const [url, setUrl] = useState('');
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['den-status'] });
    void qc.invalidateQueries({ queryKey: ['den-state'] });
    void qc.invalidateQueries({ queryKey: ['settings'] });
    void qc.invalidateQueries({ queryKey: ['module-brand'] });
  };
  const pair = useMutation({
    mutationFn: () => apiRequest<DenStatus>(`${base}/pair`, { method: 'POST', body: { pairing_url: url } }),
    onSuccess: () => {
      setUrl('');
      refresh();
      onPaired?.();
    },
  });
  const sync = useMutation({ mutationFn: () => apiRequest<DenStatus>(`${base}/sync`, { method: 'POST' }), onSettled: refresh });
  const unpair = useMutation({ mutationFn: () => apiRequest<void>(`${base}/pair`, { method: 'DELETE' }), onSuccess: refresh });
  const s = status.data;
  const error = (pair.error ?? sync.error ?? unpair.error) as Error | null;

  const pairForm = (hint: string, label: string) => (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        pair.mutate();
      }}
    >
      <p className="text-muted-foreground">{hint}</p>
      <div className="flex gap-2">
        <input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://den.example/api/v1/integrations/pairing/redeem?token=…"
          aria-label="Pairing URL"
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-3 py-2 font-mono text-xs"
        />
        <button
          type="submit"
          disabled={!url.trim() || pair.isPending}
          className="rounded-pill bg-primary px-4 py-2 font-semibold text-primary-foreground disabled:opacity-60"
        >
          {pair.isPending ? 'Connecting…' : label}
        </button>
      </div>
    </form>
  );

  const disconnect = (
    <button
      type="button"
      disabled={unpair.isPending}
      onClick={() => {
        if (confirm('Disconnect from Den? This module goes back to its own settings and default look.')) unpair.mutate();
      }}
      className="rounded-pill border border-destructive/40 px-3 py-1.5 text-destructive disabled:opacity-60"
    >
      Disconnect
    </button>
  );

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4 text-sm">
      <div>
        <h2 className="font-semibold">Den connection</h2>
        <p className="text-xs text-muted-foreground">
          Connected modules take their look and organisation settings from Den and pick up changes within a minute.
        </p>
      </div>
      {status.isPending ? (
        <p className="text-muted-foreground">Checking…</p>
      ) : s?.revoked ? (
        <>
          <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 p-3">
            <p className="font-semibold text-destructive">Den rejected this module's credential.</p>
            <p className="text-muted-foreground">
              The credential was revoked{s.revoked_at && <> on {new Date(s.revoked_at).toLocaleString()}</>}, or Wiki
              was removed in Den. Wiki keeps Den's last brand and settings but no longer syncs. Pair again manually:
              in Den, open Admin → Modules → Pair on the Wiki row and paste the new URL below.
            </p>
          </div>
          {pairForm('New pairing URL from Den:', 'Pair again')}
          <div className="flex gap-2">{disconnect}</div>
        </>
      ) : s?.paired ? (
        <>
          <p>
            Connected to <code className="font-mono">{s.den_base_url}</code>
            {s.synced_at && <> · last sync {new Date(s.synced_at).toLocaleString()}</>}
          </p>
          {s.last_error && <p className="text-destructive">Last sync failed: {s.last_error}</p>}
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => sync.mutate()}
              disabled={sync.isPending}
              className="rounded-pill border border-border px-3 py-1.5 disabled:opacity-60"
            >
              {sync.isPending ? 'Syncing…' : 'Sync now'}
            </button>
            {disconnect}
          </div>
        </>
      ) : (
        pairForm(
          'Standalone. To connect, open Den → Admin → Modules, create a pairing URL for this module and paste it here.',
          'Connect',
        )
      )}
      {error && <p className="text-destructive">{error.message}</p>}
    </section>
  );
}

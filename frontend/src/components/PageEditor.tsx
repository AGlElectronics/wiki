import { Suspense, lazy, useEffect, useState, useSyncExternalStore } from 'react';
import { cn } from '@/lib/utils';
import { LoadingRow } from '@/components/ui/States';
import { WikiProvider, type LiveState } from '@/live/WikiProvider';

const BlockEditor = lazy(() => import('@/components/BlockEditor'));

type Peer = { key: string; name: string; color: string };

function usePeers(provider: WikiProvider): Peer[] {
  const [peers, setPeers] = useState<Peer[]>([]);
  useEffect(() => {
    const { awareness } = provider;
    const read = () => {
      const byPerson = new Map<string, Peer>();
      awareness.getStates().forEach((s, id) => {
        const user = (s as { user?: { name?: string; color?: string; id?: string } }).user;
        if (id === provider.doc.clientID || !user?.name) return;
        const key = user.id || user.name;
        byPerson.set(key, { key, name: user.name, color: user.color ?? '#888' });
      });
      setPeers(Array.from(byPerson.values()));
    };
    read();
    awareness.on('change', read);
    return () => awareness.off('change', read);
  }, [provider]);
  return peers;
}

function initials(name: string) {
  const parts = name.trim().split(/\s+/);
  return ((parts[0]?.[0] ?? '') + (parts.length > 1 ? (parts.at(-1)?.[0] ?? '') : '')).toUpperCase() || '?';
}

/** Who else has the page open. */
export function Presence({ provider }: { provider: WikiProvider }) {
  const peers = usePeers(provider);
  if (peers.length === 0) return null;
  const shown = peers.slice(0, 4);
  return (
    <div className="flex -space-x-2" aria-label={`Also here: ${peers.map((p) => p.name).join(', ')}`}>
      {shown.map((p) => (
        <span
          key={p.key}
          title={p.name}
          className="w-7 h-7 rounded-full ring-2 ring-background grid place-items-center text-[11px] font-semibold text-white"
          style={{ background: p.color }}
        >
          {initials(p.name)}
        </span>
      ))}
      {peers.length > shown.length ? (
        <span className="w-7 h-7 rounded-full ring-2 ring-background grid place-items-center text-[11px] font-semibold bg-secondary text-secondary-foreground">
          +{peers.length - shown.length}
        </span>
      ) : null}
    </div>
  );
}

const STATUS_TEXT: Record<LiveState['status'], string> = {
  connecting: 'Connecting…',
  synced: 'Saved',
  offline: 'Offline, reconnecting. Keep this tab open so your changes are not lost.',
  gone: '',
  restored: 'A version was restored, reloading…',
};

export function LiveStatusText({ state }: { state: LiveState }) {
  if (state.status === 'gone') return null;
  return (
    <span
      className={cn(
        'text-xs',
        state.status === 'offline' ? 'text-warning max-w-xs' : 'text-muted-foreground whitespace-nowrap'
      )}
    >
      {STATUS_TEXT[state.status]}
    </span>
  );
}

/** One live connection per open page. */
export function useLivePage(pageId: string): { provider: WikiProvider | null; state: LiveState | null } {
  const [provider, setProvider] = useState<WikiProvider | null>(null);
  // Bumped when a restore replaces the document: the old provider's Yjs
  // doc belongs to the replaced timeline, so a fresh provider is built.
  const [epoch, setEpoch] = useState(0);
  useEffect(() => {
    const p = new WikiProvider(pageId);
    setProvider(p);
    return () => p.destroy();
  }, [pageId, epoch]);
  const state = useSyncExternalStore(
    (cb) => provider?.subscribe(cb) ?? (() => {}),
    () => provider?.getState() ?? null
  );
  useEffect(() => {
    if (state?.status === 'restored') setEpoch((e) => e + 1);
  }, [state?.status]);
  return { provider, state };
}

/** The page body: BlockNote bound to the live document. */
export function PageEditor({
  provider,
  state,
  exportRef,
}: {
  provider: WikiProvider;
  state: LiveState;
  exportRef?: React.MutableRefObject<(() => string) | null>;
}) {
  if (state.status === 'gone') {
    return <p className="text-sm text-muted-foreground">{state.message}</p>;
  }
  // The editor only starts once the stored document is in, so it never
  // adds a first block next to the one already saved.
  if (!state.loaded) {
    return (
      <div>
        <LoadingRow />
        {state.message ? <p className="text-sm text-muted-foreground text-center">{state.message}</p> : null}
      </div>
    );
  }
  return (
    <Suspense fallback={<LoadingRow />}>
      <BlockEditor provider={provider} level={state.level} exportRef={exportRef} />
      {state.message ? <p className="mt-3 text-sm text-muted-foreground">{state.message}</p> : null}
    </Suspense>
  );
}

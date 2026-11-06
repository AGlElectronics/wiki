// Ctrl+K search across titles and page text, filtered by access on the
// backend. Mounted once in the Layout.
import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { FileText, Search } from 'lucide-react';
import { searchPages } from '@/api/wiki';
import { LoadingRow } from '@/components/ui/States';
import { cn } from '@/lib/utils';

function useDebounced(value: string, ms: number): string {
  const [out, setOut] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setOut(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return out;
}

export function SearchPalette({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const debounced = useDebounced(query.trim(), 200);
  const results = useQuery({
    queryKey: ['search', debounced],
    queryFn: ({ signal }) => searchPages(debounced, signal),
    enabled: debounced.length > 0,
    placeholderData: (prev) => prev,
  });
  const hits = debounced ? (results.data ?? []) : [];

  useEffect(() => {
    inputRef.current?.focus();
  }, []);
  useEffect(() => {
    setActive(0);
  }, [debounced]);

  const open = (pageId: string) => {
    onClose();
    void navigate({ to: '/p/$pageId', params: { pageId } });
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((a) => Math.min(a + 1, hits.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((a) => Math.max(a - 1, 0));
    } else if (e.key === 'Enter' && hits[active]) {
      e.preventDefault();
      open(hits[active].id);
    } else if (e.key === 'Escape') {
      onClose();
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 p-4 pt-[12vh]"
      role="dialog"
      aria-modal="true"
      aria-label="Search"
      onClick={onClose}
    >
      <div
        className="w-full max-w-xl rounded-card border border-border bg-card shadow-lg overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-border px-4">
          <Search className="w-4 h-4 text-muted-foreground shrink-0" />
          <input
            ref={inputRef}
            value={query}
            placeholder="Search pages…"
            aria-label="Search pages"
            className="w-full bg-transparent py-3 text-sm outline-none"
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
          />
          <kbd className="hidden sm:block text-[10px] text-muted-foreground border border-border rounded px-1">esc</kbd>
        </div>
        <div className="max-h-[50vh] overflow-y-auto p-1">
          {results.isLoading && debounced ? <LoadingRow /> : null}
          {debounced && results.data && hits.length === 0 && !results.isLoading ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">Nothing found for “{debounced}”.</p>
          ) : null}
          {!debounced ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">
              Type to search titles and page text.
            </p>
          ) : null}
          {hits.map((hit, i) => (
            <button
              key={hit.id}
              type="button"
              className={cn(
                'w-full flex items-start gap-3 rounded-md px-3 py-2 text-left text-sm',
                i === active ? 'bg-secondary' : 'hover:bg-secondary/60'
              )}
              onMouseEnter={() => setActive(i)}
              onClick={() => open(hit.id)}
            >
              <span className="mt-0.5 shrink-0">
                {hit.icon ? hit.icon : <FileText className="w-4 h-4 text-muted-foreground" />}
              </span>
              <span className="min-w-0">
                <span className="block truncate font-medium">{hit.title.trim() || 'Untitled'}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  {hit.space_name}
                  {hit.snippet ? (
                    <>
                      {' · '}
                      {/* ts_headline marks matches with <b>…</b>; render just the text */}
                      {hit.snippet.replaceAll('<b>', '').replaceAll('</b>', '')}
                    </>
                  ) : null}
                </span>
              </span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

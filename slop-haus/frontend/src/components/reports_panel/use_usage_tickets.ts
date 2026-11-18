import { useCallback, useEffect, useRef, useState } from "react";
import { type LlmUsageTicket, type LlmUsageTicketPage } from "@/api";
import { errorMessage } from "@/lib/use_async_action";

export interface UsageTicketsView {
  items: readonly LlmUsageTicket[];
  // Every ticket the filter matches, not just those loaded.
  total: number;
  hasMore: boolean;
  // The first page has arrived, or the query matches nothing.
  loaded: boolean;
  loading: boolean;
  error: string | null;
  loadMore(): void;
  retry(): void;
}

interface State {
  items: readonly LlmUsageTicket[];
  total: number;
  cursor: string | undefined;
  hasMore: boolean;
  loaded: boolean;
  loading: boolean;
  error: string | null;
}

const INITIAL: State = { items: [], total: 0, cursor: undefined, hasMore: false, loaded: false, loading: true, error: null };

// Pages the by-ticket usage list by the server's cursor. One query for the
// hook's lifetime: a different query is a different instance (the caller
// keys the component that holds it), so a reply to an old query can never
// land on a new list. Replies are ignored once a newer request was made.
export function useUsageTickets(
  fetchPage: (cursor: string | undefined) => Promise<LlmUsageTicketPage>,
  // False when the query can match nothing, which needs no request.
  enabled: boolean,
): UsageTicketsView {
  const [state, setState] = useState<State>(enabled ? INITIAL : { ...INITIAL, loaded: true, loading: false });
  // The page being fetched. A retry repeats it; loading more moves it on.
  const [request, setRequest] = useState<{ cursor: string | undefined; attempt: number }>({ cursor: undefined, attempt: 0 });
  const fetchRef = useRef(fetchPage);
  fetchRef.current = fetchPage;

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    setState((prev) => ({ ...prev, loading: true, error: null }));
    fetchRef
      .current(request.cursor)
      .then((page) => {
        if (cancelled) return;
        setState((prev) => {
          // A ticket whose usage moved while paging can come round again.
          const seen = new Set(prev.items.map((t) => t.id));
          return {
            items: [...prev.items, ...page.tickets.filter((t) => !seen.has(t.id))],
            total: page.total,
            cursor: page.next_cursor,
            hasMore: page.next_cursor !== undefined,
            loaded: true,
            loading: false,
            error: null,
          };
        });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const error = errorMessage(err, "Failed to load tickets.");
        setState((prev) => ({ ...prev, loading: false, error }));
      });
    return () => {
      cancelled = true;
    };
  }, [request, enabled]);

  const { cursor, hasMore, loading, loaded } = state;
  const loadMore = useCallback(() => {
    if (!enabled || !loaded || loading || !hasMore) return;
    setRequest((prev) => ({ cursor, attempt: prev.attempt + 1 }));
  }, [enabled, loaded, loading, hasMore, cursor]);
  const retry = useCallback(() => setRequest((prev) => ({ ...prev, attempt: prev.attempt + 1 })), []);

  return { ...state, loadMore, retry };
}

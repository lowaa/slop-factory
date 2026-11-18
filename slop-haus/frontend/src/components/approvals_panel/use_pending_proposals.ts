import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { apiClient, type Proposal, type ProposalTicketFilter } from "@/api";
import { errorMessage } from "@/lib/use_async_action";

const PAGE_SIZE = 50;
// The server's page ceiling.
const MAX_PAGE_SIZE = 100;

export interface PendingProposals {
  status: "loading" | "error" | "loaded";
  error: string | null;
  // Oldest first, the pages loaded so far.
  proposals: Proposal[];
  hasMore: boolean;
  loadingMore: boolean;
  loadMore: () => void;
}

interface Fetched {
  // What the pages were fetched for, so they are only shown under that filter.
  filterKey: string;
  status: PendingProposals["status"];
  error: string | null;
  proposals: Proposal[];
  cursor: string | undefined;
  loadingMore: boolean;
}

const NONE: Fetched = { filterKey: "", status: "loading", error: null, proposals: [], cursor: undefined, loadingMore: false };

// refetch is the first `want` proposals, a request's worth at a time: a
// refresh restores as many pages as were loaded, however many that was.
// null when `superseded` says a newer request has made the answer moot.
async function refetch(
  filter: ProposalTicketFilter,
  want: number,
  superseded: () => boolean,
): Promise<{ proposals: Proposal[]; cursor: string | undefined } | null> {
  const proposals: Proposal[] = [];
  let cursor: string | undefined;
  do {
    const limit = Math.min(want - proposals.length, MAX_PAGE_SIZE);
    const page = await apiClient.listPendingProposalsPage(filter, { limit, ...(cursor !== undefined && { cursor }) });
    if (superseded()) return null;
    proposals.push(...page.proposals);
    cursor = page.nextCursor;
    // A short page is the end of what there is, whatever cursor came with it.
    if (page.proposals.length < limit) break;
  } while (cursor !== undefined && proposals.length < want);
  return { proposals, cursor };
}

function failure(err: unknown, fallback: string): string {
  return errorMessage(err, fallback);
}

// usePendingProposals is the pending proposals the filter lets through, a
// page at a time. The shared list (the tab's badge, a ticket's strip) is
// everything; this is the page's own, so it is fetched here and refetched
// whenever `refreshOn` changes, which is how a decision or the board's stream
// reaches it. A refresh asks for as many as are loaded, so deciding a card
// does not send a person who has paged on back to the top.
export function usePendingProposals(filter: ProposalTicketFilter | null, refreshOn: unknown): PendingProposals {
  const filterKey = JSON.stringify(filter);
  const [state, setState] = useState<Fetched>(NONE);
  // Only the latest request's answer is used: an older one landing late would
  // put back a proposal that has since been decided.
  const issued = useRef(0);
  const moreInFlight = useRef(false);
  const loaded = useRef(0);
  loaded.current = state.filterKey === filterKey ? state.proposals.length : 0;
  const stable = useMemo(() => filter, [filterKey]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const request = ++issued.current;
    if (stable === null) {
      setState({ ...NONE, filterKey, status: "loaded" });
      return;
    }
    const want = Math.max(loaded.current, PAGE_SIZE);
    refetch(stable, want, () => request !== issued.current)
      .then((fetched) => {
        if (!fetched) return;
        setState({ filterKey, status: "loaded", error: null, proposals: fetched.proposals, cursor: fetched.cursor, loadingMore: false });
      })
      .catch((err: unknown) => {
        if (request !== issued.current) return;
        setState((was) => {
          const keep = was.filterKey === filterKey;
          return {
            ...(keep ? was : NONE),
            filterKey,
            loadingMore: false,
            status: keep && was.status === "loaded" ? "loaded" : "error",
            error: failure(err, "Failed to load proposals."),
          };
        });
      });
    // filterKey stands for the filter: equal filters do not refetch on their own.
  }, [stable, filterKey, refreshOn]);

  const cursor = state.cursor;
  const loadMore = useCallback(() => {
    if (stable === null || cursor === undefined || moreInFlight.current) return;
    // Not a new number: a refresh that lands first replaces the pages this
    // would extend, so it cancels this, and not the other way round.
    const request = issued.current;
    moreInFlight.current = true;
    setState((was) => ({ ...was, loadingMore: true, error: null }));
    apiClient
      .listPendingProposalsPage(stable, { limit: PAGE_SIZE, cursor })
      .then((page) => {
        moreInFlight.current = false;
        if (request !== issued.current) return;
        setState((was) => {
          const have = new Set(was.proposals.map((p) => p.id));
          const fresh = page.proposals.filter((p) => !have.has(p.id));
          return { ...was, proposals: [...was.proposals, ...fresh], cursor: page.nextCursor, loadingMore: false };
        });
      })
      .catch((err: unknown) => {
        moreInFlight.current = false;
        if (request !== issued.current) return;
        setState((was) => ({ ...was, loadingMore: false, error: failure(err, "Failed to load more proposals.") }));
      });
  }, [stable, cursor]);

  // A list fetched under another filter is not shown, nor acted on, while the
  // new one is on its way: its cards would not be what the filter says.
  if (state.filterKey !== filterKey) {
    return { status: "loading", error: null, proposals: [], hasMore: false, loadingMore: false, loadMore };
  }
  return {
    status: state.status,
    error: state.error,
    proposals: state.proposals,
    hasMore: state.cursor !== undefined,
    loadingMore: state.loadingMore,
    loadMore,
  };
}

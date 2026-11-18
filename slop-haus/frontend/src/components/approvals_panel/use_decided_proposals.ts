import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  apiClient,
  type DecidedProposalState,
  type Proposal,
  type ProposalTicketFilter,
} from "@/api";
import { errorMessage } from "@/lib/use_async_action";

const PAGE_SIZE = 50;

export interface DecidedProposals {
  status: "loading" | "error" | "loaded";
  error: string | null;
  // Newest first, the pages loaded so far.
  proposals: Proposal[];
  hasMore: boolean;
  loadingMore: boolean;
  loadMore: () => void;
}

interface Fetched {
  // What the pages were fetched for, so they are only shown under that query.
  key: string;
  status: DecidedProposals["status"];
  error: string | null;
  proposals: Proposal[];
  cursor: string | undefined;
  loadingMore: boolean;
}

const NONE: Fetched = { key: "", status: "loading", error: null, proposals: [], cursor: undefined, loadingMore: false };

function failure(err: unknown, fallback: string): string {
  return errorMessage(err, fallback);
}

// useDecidedProposals is the proposals in one decided state that the ticket
// filter lets through, newest first, a page at a time. Unlike the pending
// list it is history: it is fetched when the query changes and not on the
// board's stream, and a cursor is only valid for the query it came from.
export function useDecidedProposals(filter: ProposalTicketFilter | null, state: DecidedProposalState): DecidedProposals {
  const key = JSON.stringify([filter, state]);
  const [fetched, setFetched] = useState<Fetched>(NONE);
  const issued = useRef(0);
  const moreInFlight = useRef(false);
  const stable = useMemo(() => filter, [key]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const request = ++issued.current;
    moreInFlight.current = false;
    if (stable === null) {
      setFetched({ ...NONE, key, status: "loaded" });
      return;
    }
    setFetched({ ...NONE, key });
    apiClient
      .listDecidedProposalsPage(stable, state, { limit: PAGE_SIZE })
      .then((page) => {
        if (request !== issued.current) return;
        setFetched({ key, status: "loaded", error: null, proposals: page.proposals, cursor: page.nextCursor, loadingMore: false });
      })
      .catch((err: unknown) => {
        if (request !== issued.current) return;
        setFetched({ ...NONE, key, status: "error", error: failure(err, "Failed to load proposals.") });
      });
  }, [stable, state, key]);

  const cursor = fetched.cursor;
  const loadMore = useCallback(() => {
    if (stable === null || cursor === undefined || moreInFlight.current) return;
    const request = issued.current;
    moreInFlight.current = true;
    setFetched((was) => ({ ...was, loadingMore: true, error: null }));
    apiClient
      .listDecidedProposalsPage(stable, state, { limit: PAGE_SIZE, cursor })
      .then((page) => {
        if (request !== issued.current) return;
        moreInFlight.current = false;
        setFetched((was) => ({ ...was, proposals: [...was.proposals, ...page.proposals], cursor: page.nextCursor, loadingMore: false }));
      })
      .catch((err: unknown) => {
        if (request !== issued.current) return;
        moreInFlight.current = false;
        setFetched((was) => ({ ...was, loadingMore: false, error: failure(err, "Failed to load more proposals.") }));
      });
  }, [stable, state, cursor]);

  if (fetched.key !== key) {
    return { status: "loading", error: null, proposals: [], hasMore: false, loadingMore: false, loadMore };
  }
  return {
    status: fetched.status,
    error: fetched.error,
    proposals: fetched.proposals,
    hasMore: fetched.cursor !== undefined,
    loadingMore: fetched.loadingMore,
    loadMore,
  };
}

import { useMemo, type ReactNode } from "react";
import { apiClient } from "@/api";
import { liveKeys, useLiveInvalidate } from "@/lib/live";
import { useResource } from "@/lib/resource";
import { ProposalsContext, type ProposalsValue } from "./proposals";

let fetches = 0;
const LIVE_KEYS = [liveKeys.proposalsPending()];

// ProposalsProvider fetches the pending proposals and keeps them current
// with the board's stream: the server bumps the board's revision whenever a
// proposal is made or decided.
export function ProposalsProvider({ children }: { children: ReactNode }) {
  // Each response is wrapped with its fetch's number, so it is a new value
  // even when equal to the last (which the cache would otherwise collapse):
  // the approvals page refetches its own pages whenever this list changes.
  const resource = useResource(
    liveKeys.proposalsPending(),
    async () => ({ fetch: ++fetches, list: await apiClient.listProposals("pending") }),
    { fallbackError: "Failed to load proposals." },
  );
  useLiveInvalidate(LIVE_KEYS);

  const { status, error, reload } = resource;
  const data = resource.data?.list;
  const fetched = resource.data?.fetch;
  const value = useMemo<ProposalsValue>(
    () => ({
      // A later failure keeps what was loaded, as the list is not worth an error over.
      status: data !== undefined ? "loaded" : status === "error" ? "error" : "loading",
      error: data !== undefined ? null : error,
      // The server lists newest first.
      proposals: data ? [...data].reverse() : [],
      reload,
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [status, fetched, error, reload],
  );
  return <ProposalsContext.Provider value={value}>{children}</ProposalsContext.Provider>;
}

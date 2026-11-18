import { useCallback, useEffect, useRef } from "react";
import { hashKey, useQuery } from "@tanstack/react-query";
import { errorMessage } from "../use_async_action";
import { defaultResourceClient, useResourceClient } from "./client";
import type { ResourceKey } from "./keys";

export type ResourceStatus = "loading" | "ready" | "error";

export interface Resource<T> {
  status: ResourceStatus;
  // The last good response. It survives a later failure, so a view keeps
  // showing what it knew while "error" says the refresh did not work.
  data: T | undefined;
  error: string | null;
  // Fetches now, whatever the interval; resolves once the response has been
  // applied or dropped as stale.
  reload: () => Promise<void>;
}

export interface ResourceOptions {
  // Refetch this often while the tab is visible. Omit to fetch once.
  pollMs?: number;
  // Keep the value in the cache after the last view using it unmounts, so the
  // next one shows it at once. False for a key only one view will ever read.
  keepUnused?: boolean;
  // False holds the fetch back (a view waiting on an id).
  enabled?: boolean;
  // What to show when the fetch throws something that is not an ApiError.
  fallbackError?: string;
  // Words a failure in place of the default, for one a view explains better.
  describeError?: (err: unknown) => string;
}

// useResource is the app's one door to server state. Views asking for the
// same key share one cached value and one in-flight request.
export function useResource<T>(
  key: ResourceKey,
  fetcher: () => Promise<T>,
  { pollMs, enabled = true, keepUnused = true, fallbackError = "Could not load.", describeError }: ResourceOptions = {},
): Resource<T> {
  const client = useResourceClient();
  // The query reads the latest closure through a ref, so an inline fetcher
  // does not restart anything.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const describeRef = useRef(describeError);
  describeRef.current = describeError;

  const query = useQuery<T, unknown, T, ResourceKey>(
    { queryKey: key, queryFn: () => fetcherRef.current(), enabled, ...(keepUnused ? {} : { gcTime: 0 }) },
    client,
  );
  const { refetch } = query;
  const dataRef = useRef(query.data);
  dataRef.current = query.data;

  // Cancelling the request in flight is the stale guard: a slow earlier
  // response cannot land after a newer one and put back older data. It also
  // makes reload (and a poll tick) mean "fetch now" -- TanStack would otherwise hand a reload
  // during the first load the request already in flight.
  const reload = useCallback(async () => {
    await client.cancelQueries({ queryKey: key }, { revert: true });
    await refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, hashKey(key), refetch]);

  // Hidden tabs skip the tick, and catching up on becoming visible means a
  // long-hidden tab is not stale until the next interval.
  useEffect(() => {
    if (pollMs === undefined || !enabled) return;
    const onVisible = () => {
      if (document.visibilityState !== "visible") return;
      // A request that is slow but has data to show is left to land, so a
      // fetch slower than the interval is not cancelled at every tick and
      // views sharing the key do not each restart it. With nothing to show a
      // stuck first load is superseded instead.
      if (dataRef.current !== undefined) void refetch({ cancelRefetch: false });
      else void reload();
    };
    const timer = setInterval(onVisible, pollMs);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [pollMs, enabled, reload, refetch]);

  const status: ResourceStatus = query.isError ? "error" : query.data === undefined && query.isPending ? "loading" : "ready";
  return {
    status,
    data: query.data,
    error: query.isError ? (describeRef.current?.(query.error) ?? errorMessage(query.error, fallbackError)) : null,
    reload,
  };
}

// Marks the key stale and refetches it where a view is showing it.
// With restart, a read in flight is cancelled first, so the refetch starts
// after the event that prompted it -- TanStack would otherwise hand a first
// load that has no value yet the request already running, which may predate it.
export async function invalidate(key: ResourceKey, client = defaultResourceClient, { restart = false } = {}): Promise<void> {
  if (restart) await client.cancelQueries({ queryKey: key }, { revert: true });
  await client.invalidateQueries({ queryKey: key });
}

// Replaces what the key holds, for a write whose result is already known. A
// read still in flight is cancelled first: it began before the write, so
// landing after it would put the older value back.
export async function setResource<T>(key: ResourceKey, updater: (current: T | undefined) => T, client = defaultResourceClient): Promise<void> {
  await client.cancelQueries({ queryKey: key }, { revert: true });
  client.setQueryData<T>(key, updater);
}

// Reads a key outside a view: a request already in flight is shared, and the
// answer is cached for the views of that key. Never answers from the cache
// alone, since the caller wants what the server says now.
export function readResource<T>(key: ResourceKey, fetcher: () => Promise<T>, client = defaultResourceClient): Promise<T> {
  return client.fetchQuery({ queryKey: key, queryFn: fetcher, staleTime: 0 });
}

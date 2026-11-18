import { useId, useRef, type DependencyList } from "react";
import { resourceKeys, useResource } from "./resource";

export type PolledStatus = "loading" | "ready" | "error";

export interface PolledResource<T> {
  status: PolledStatus;
  // The last good response. It survives a later failure, so a view keeps
  // showing what it knew while "error" says the refresh did not work.
  data: T | undefined;
  error: string | null;
  // Fetches now, whatever the interval; resolves once the response has been
  // applied or dropped as stale.
  reload: () => Promise<void>;
}

export interface PolledResourceOptions {
  // Refetch this often while the tab is visible. Omit to fetch once per deps.
  intervalMs?: number;
  // Changing any of these discards the data and starts again, as
  // useEffect's deps would.
  deps?: DependencyList;
  // What to show when the fetch throws something that is not an ApiError.
  fallbackError?: string;
  // Words a failure in place of the default (an ApiError's own message, else
  // fallbackError), for one a view explains better, such as a timeout.
  describeError?: (err: unknown) => string;
}

// usePolledResource keeps its original signature over useResource, so the
// views written against it move onto the shared cache without edits. Each
// hook instance owns a private key, bumped when deps change, which is what
// discards the data and starts again as useEffect's deps would.
export function usePolledResource<T>(
  fetcher: () => Promise<T>,
  { intervalMs, deps = [], fallbackError, describeError }: PolledResourceOptions = {},
): PolledResource<T> {
  const instance = useId();
  const generation = useRef(0);
  const lastDeps = useRef(deps);
  if (lastDeps.current.length !== deps.length || lastDeps.current.some((d, i) => !Object.is(d, deps[i]))) {
    lastDeps.current = deps;
    generation.current += 1;
  }
  const key = resourceKeys.polled(instance, generation.current);

  return useResource(key, fetcher, { pollMs: intervalMs, fallbackError, describeError, keepUnused: false });
}

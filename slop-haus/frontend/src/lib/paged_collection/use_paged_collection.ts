import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { PagedCollection, type PagedCollectionDeps, type PagedSnapshot } from "./paged_collection";
import type { TicketQuery } from "../ticket_query/ticket_query";

const EMPTY: PagedSnapshot = { items: [], loading: false, loadingMore: false, hasMore: false, loaded: false, error: null };
const NO_UNSUBSCRIBE = () => {};
const noSubscribe = () => NO_UNSUBSCRIBE;

export interface PagedCollectionView extends PagedSnapshot {
  loadMore(): void;
  retry(): void;
  reload(): void;
}

// A PagedCollection for the component's lifetime. A new `deps.key` is a new
// list; a new query is a reset of the same one. Everything else in `deps` is
// read as of the latest render, so callers need not memoise it.
export function usePagedCollection(deps: PagedCollectionDeps, query: TicketQuery): PagedCollectionView {
  const latest = useRef({ deps, query });
  latest.current = { deps, query };
  const [collection, setCollection] = useState<PagedCollection | null>(null);

  useEffect(() => {
    const current = () => latest.current.deps;
    const made = new PagedCollection({
      key: deps.key,
      queryModel: current().queryModel,
      logger: current().logger,
      matcher: current().matcher,
      fetchPage: (params, signal) => current().fetchPage(params, signal),
      subscribe: deps.subscribe && ((changes) => current().subscribe?.(changes) ?? NO_UNSUBSCRIBE),
    });
    made.start(latest.current.query);
    setCollection(made);
    return () => made.close();
    // A new key is a new list; the rest is read through `latest`.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deps.key]);

  const queryKey = deps.queryModel.key(query);
  useEffect(() => {
    collection?.setQuery(latest.current.query);
  }, [collection, queryKey]);

  const snapshot = useSyncExternalStore(collection?.subscribe ?? noSubscribe, collection?.getSnapshot ?? (() => EMPTY));
  const loadMore = useCallback(() => collection?.loadMore(), [collection]);
  const retry = useCallback(() => collection?.retry(), [collection]);
  const reload = useCallback(() => collection?.reload(), [collection]);
  return { ...snapshot, loadMore, retry, reload };
}

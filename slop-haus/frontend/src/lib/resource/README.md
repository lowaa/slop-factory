# lib/resource

The app's server-state cache: TanStack Query behind our own names. Nothing
outside this directory imports `@tanstack/react-query`.

- `useResource(key, fetcher, {pollMs?, enabled?})` returns
  `{status, data, error, reload}`. Views using the same key share one cached
  value and one request.
- `invalidate(key)` marks a key stale and refetches it where it is on screen;
  `setResource(key, updater)` writes a known result without a fetch.
- Keys live in `keys.ts` (`resourceKeys`). Add the key there, never inline.
- `reload()` cancels the request in flight, so a slow earlier response can't
  overwrite a newer one. Polling skips hidden tabs and catches up on becoming
  visible.
- `ResourceProvider` wraps the app (`main.tsx`). Without one, views use the
  shared `defaultResourceClient`, so stories and tests need no wiring; the
  test setup clears it after each test.
- `usePolledResource` (`lib/use_polled_resource.ts`) keeps its old signature
  on top of this, with a private key per hook instance.
- `ResourceView` (`resource_view.tsx`) renders the standard loading
  (`role="status"`), error-with-retry (`role="alert"`) and empty states over a
  `useResource`/`usePolledResource` result. The reports panel is converted;
  other views convert as they are edited.
- `PanelErrorBoundary` (`lib/error_boundary/`) wraps an independent panel so a
  render error stays in it; the board columns and each reports section use it.
- `mutateResource(key, {optimistic, request, commit, rollback})` runs a write
  over a cached value: the optimistic value shows at once, the reply is folded
  in by `commit`, and a failure runs `rollback` before rethrowing.
  `updateResource` is the synchronous cancel-and-write underneath it.
- `invalidate(key, client, {restart: true})` cancels a read in flight first,
  for an event that must not be answered by a request that predates it.
- The ticket page's store (`components/ticket_detail/use_ticket_store.ts`)
  keeps the ticket, comments, handoffs and revisions under the
  `["ticket", id]` keys.

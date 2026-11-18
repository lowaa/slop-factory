import { useCallback } from "react";
import { useRouter } from "@slop-factory/router";

// useRouteActive returns a check, read when called, of whether the route with
// this id is still one of the router's matches. A callback that fires late
// (a filter's debounce) uses it to do nothing once the user has left: a
// route navigating from its own path would otherwise drag them back. The
// page can outlive the navigation away -- a lazy route's chunk loads while
// the old page stays on screen -- so being mounted does not say it is current.
export function useRouteActive(routeId: string): () => boolean {
  const router = useRouter();
  return useCallback(
    () => router.state.matches.some((match) => match.routeId === routeId),
    [router, routeId],
  );
}

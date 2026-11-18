import { createContext, useContext } from "react";
import { QueryClient, notifyManager } from "@tanstack/react-query";

// TanStack defers its notifications with setTimeout, which a test using fake
// timers never fires; a microtask is as batched and needs no timer.
notifyManager.setScheduler((callback) => queueMicrotask(callback));

export type ResourceClient = QueryClient;

export function createResourceClient(): ResourceClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // A failed fetch is reported, not retried behind the view's back:
        // views poll or offer a reload themselves.
        retry: false,
        // The views decide when to refetch (reload, poll, invalidate), so
        // nothing refetches on focus or remount by itself.
        staleTime: Infinity,
        refetchOnWindowFocus: false,
        refetchOnReconnect: false,
      },
    },
  });
}

// The app-wide cache. Used when no ResourceProvider is above, so a story or
// test renders a view without wiring one, and invalidate()/setResource() have
// a cache to act on from outside React.
export const defaultResourceClient = createResourceClient();

export const ResourceClientContext = createContext<ResourceClient>(defaultResourceClient);

export function useResourceClient(): ResourceClient {
  return useContext(ResourceClientContext);
}

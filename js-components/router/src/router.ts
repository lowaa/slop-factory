import {
  createBrowserHistory,
  createMemoryHistory,
  createRouter,
  type AnyRoute,
  type ErrorRouteComponent,
  type RouterHistory,
} from "@tanstack/react-router";
import { parseSearch, stringifySearch } from "./search";

export type HistoryOption = { kind: "browser" } | { kind: "memory"; initialEntries: string[] };

export interface CreateAppRouterOptions<TRouteTree extends AnyRoute, TContext> {
  routeTree: TRouteTree;
  context: TContext;
  // Browser history (the address bar) unless a test asks for memory.
  history?: HistoryOption;
  // What a route that throws -- rendering, loading or in a guard -- shows
  // instead of the router's bare default. The router catches those itself,
  // so an app's own error boundary above the router never sees them.
  errorComponent?: ErrorRouteComponent;
}

function historyFor(option: HistoryOption | undefined): RouterHistory {
  if (option?.kind === "memory") return createMemoryHistory({ initialEntries: option.initialEntries });
  return createBrowserHistory();
}

// createAppRouter is the one place an app's router is configured, so every
// app gets the same URL handling: flat string search params (see
// search.ts) and no preloading on hover -- a guard's beforeLoad runs on a
// preload too, and guards here have side effects on the app's own state.
export function createAppRouter<TRouteTree extends AnyRoute, TContext>(
  options: CreateAppRouterOptions<TRouteTree, TContext>,
) {
  return createRouter({
    routeTree: options.routeTree,
    context: options.context,
    history: historyFor(options.history),
    parseSearch,
    stringifySearch,
    // The router's default already; spelled out because it decides what
    // URLs look like, which an app should not inherit silently.
    trailingSlash: "never",
    defaultPreload: false,
    defaultErrorComponent: options.errorComponent,
  });
}

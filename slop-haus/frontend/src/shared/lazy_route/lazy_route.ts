import { lazy, type ComponentType, type LazyExoticComponent } from "react";

const loaders = new Set<() => Promise<unknown>>();

// React.lazy for a route page, remembering its loader so tests can resolve
// every route chunk up front (preloadRoutes) instead of racing the first
// navigation against a module transform on a loaded CI runner.
// ComponentType<any> is React.lazy's own bound; <never> rejects components with props.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function lazyRoute<T extends ComponentType<any>>(
  load: () => Promise<{ default: T }>,
): LazyExoticComponent<T> {
  loaders.add(load);
  return lazy(load);
}

// Only meaningful once the app's modules have been imported, since a loader
// registers when its lazyRoute call evaluates.
export async function preloadRoutes(): Promise<void> {
  await Promise.all([...loaders].map((load) => load()));
}

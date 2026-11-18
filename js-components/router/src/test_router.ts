import { createContext, type ReactNode } from "react";
import { Outlet, createRootRoute, createRoute, type RouteComponent } from "@tanstack/react-router";
import { createAppRouter } from "./router";

// What the test router renders at every path. Handed in through context
// rather than baked into the route when the router is made, so a rerender
// (or a story's changed args) shows the new ui in the same router.
export const UiContext = createContext<ReactNode>(null);

// renderUi is the component that shows UiContext's ui; it lives with the
// other components (test_router_provider.tsx) and is passed in, so this
// file exports no component of its own.
export function createTestRouter(initialPath: string, renderUi: RouteComponent) {
  const rootRoute = createRootRoute({ component: Outlet });
  // A splat route renders ui at every path, so a test can start anywhere
  // and navigate anywhere without declaring routes it doesn't care about.
  const everywhere = createRoute({ getParentRoute: () => rootRoute, path: "$", component: renderUi });
  return createAppRouter({
    routeTree: rootRoute.addChildren([everywhere]),
    context: {},
    history: { kind: "memory", initialEntries: [initialPath] },
  });
}

export type TestRouter = ReturnType<typeof createTestRouter>;

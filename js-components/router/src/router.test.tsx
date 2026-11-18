import { describe, expect, it, onTestFinished, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, Outlet, RouterProvider, createRootRouteWithContext, createRoute } from "@tanstack/react-router";
import { createAppRouter, type HistoryOption } from "./router";
import { redirectTo, requireAuthenticated } from "./guards";
import { noSearch, optionalString } from "./search";
import { positiveIntParam } from "./params";

interface TestContext {
  authenticated: boolean;
  pendingPath: string | null;
}

// A small app: a login page, a guarded home page, and a page with typed
// search params -- enough to drive the package's factory and guards the
// way an app's route tree would. onPendingLoad counts /pending's guard
// runs, which a preload would add to.
function buildRouteTree(onPendingLoad: () => void = () => {}) {
  const rootRoute = createRootRouteWithContext<TestContext>()({ component: Outlet });
  const login = createRoute({
    getParentRoute: () => rootRoute,
    path: "/login",
    validateSearch: noSearch,
    component: () => <h1>Login</h1>,
  });
  const home = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    validateSearch: noSearch,
    beforeLoad: ({ context }) => {
      requireAuthenticated({ authenticated: context.authenticated, loginPath: "/login" });
      if (context.pendingPath) redirectTo(context.pendingPath);
    },
    component: () => (
      <>
        <h1>Home</h1>
        <Link to="/pending">Pending</Link>
      </>
    ),
  });
  const pending = createRoute({
    getParentRoute: () => rootRoute,
    path: "/pending",
    validateSearch: noSearch,
    beforeLoad: onPendingLoad,
    component: () => <h1>Pending</h1>,
  });
  const device = createRoute({
    getParentRoute: () => rootRoute,
    path: "/device",
    validateSearch: (raw: Record<string, unknown>) => ({ code: optionalString(raw, "code") }),
    component: function Device() {
      const { code } = device.useSearch();
      return <h1>Device {typeof code}:{code}</h1>;
    },
  });
  const item = createRoute({
    getParentRoute: () => rootRoute,
    path: "/items/$itemId",
    params: positiveIntParam("itemId"),
    validateSearch: noSearch,
    beforeLoad: ({ context, location }) => {
      requireAuthenticated({ authenticated: context.authenticated, loginPath: "/login", returnTo: location.href });
    },
    component: function Item() {
      const { itemId } = item.useParams();
      return <h1>Item {typeof itemId}:{itemId}</h1>;
    },
  });
  const orgItem = createRoute({
    getParentRoute: () => rootRoute,
    path: "/orgs/$orgId/items/$itemId",
    params: positiveIntParam<"itemId", "orgId">("itemId"),
    validateSearch: noSearch,
    component: function OrgItem() {
      const { orgId, itemId } = orgItem.useParams();
      return (
        <>
          <h1>
            Org {orgId} item {typeof itemId}:{itemId}
          </h1>
          <Link to="/orgs/$orgId/items/$itemId" params={{ orgId: "beta", itemId: 7 }}>
            Other org
          </Link>
        </>
      );
    },
  });
  const unknown = createRoute({
    getParentRoute: () => rootRoute,
    path: "$",
    component: () => <h1>Unknown</h1>,
  });
  return rootRoute.addChildren([login, home, pending, device, item, orgItem, unknown]);
}

function renderRouter(context: TestContext, history: HistoryOption | undefined, onPendingLoad?: () => void) {
  const router = createAppRouter({ routeTree: buildRouteTree(onPendingLoad), context, history });
  onTestFinished(() => router.history.destroy());
  render(<RouterProvider router={router} />);
  return router;
}

async function renderAt(path: string, context: TestContext, onPendingLoad?: () => void) {
  return renderRouter(context, { kind: "memory", initialEntries: [path] }, onPendingLoad);
}

describe("createAppRouter", () => {
  it("renders the route at the initial memory-history path", async () => {
    await renderAt("/", { authenticated: true, pendingPath: null });
    expect(await screen.findByRole("heading", { name: "Home" })).toBeInTheDocument();
  });

  it("hands validateSearch string values and leaves the URL as it came", async () => {
    const router = await renderAt("/device?code=007", { authenticated: true, pendingPath: null });
    expect(await screen.findByRole("heading", { name: "Device string:007" })).toBeInTheDocument();
    expect(router.state.location.href).toBe("/device?code=007");
  });

  // The router is not in strict search mode, so a URL arriving with params
  // its route has not declared (yet) keeps them rather than being rewritten.
  it("keeps a search param the route does not declare", async () => {
    const router = await renderAt("/login?extra=1", { authenticated: false, pendingPath: null });
    await screen.findByRole("heading", { name: "Login" });
    expect(router.state.location.href).toBe("/login?extra=1");
  });
});

describe("createAppRouter errorComponent", () => {
  function throwingRouter(errorComponent?: () => React.ReactElement) {
    const rootRoute = createRootRouteWithContext<TestContext>()({ component: Outlet });
    const boom = createRoute({
      getParentRoute: () => rootRoute,
      path: "/boom",
      component: () => {
        throw new Error("render failed");
      },
    });
    const router = createAppRouter({
      routeTree: rootRoute.addChildren([boom]),
      context: { authenticated: true, pendingPath: null },
      history: { kind: "memory", initialEntries: ["/boom"] },
      errorComponent,
    });
    onTestFinished(() => router.history.destroy());
    render(<RouterProvider router={router} />);
  }

  it("shows the app's component for a route that throws while rendering", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    throwingRouter(() => <h1>Something went wrong</h1>);
    expect(await screen.findByRole("heading", { name: "Something went wrong" })).toBeInTheDocument();
  });
});

describe("positiveIntParam routes", () => {
  it("hands the route's components a number", async () => {
    await renderAt("/items/42", { authenticated: true, pendingPath: null });
    expect(await screen.findByRole("heading", { name: "Item number:42" })).toBeInTheDocument();
  });

  it("keeps a route's other path params, parsing and building links", async () => {
    await renderAt("/orgs/acme/items/42", { authenticated: true, pendingPath: null });
    expect(await screen.findByRole("heading", { name: "Org acme item number:42" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Other org" })).toHaveAttribute("href", "/orgs/beta/items/7");
  });

  it("does not match a path whose param is not a positive integer", async () => {
    await renderAt("/items/abc", { authenticated: true, pendingPath: null });
    expect(await screen.findByRole("heading", { name: "Unknown" })).toBeInTheDocument();
  });
});

describe("createAppRouter history", () => {
  it("uses the browser's own history by default", async () => {
    window.history.replaceState(null, "", "/");
    const router = renderRouter({ authenticated: true, pendingPath: null }, undefined);
    await screen.findByRole("heading", { name: "Home" });

    await userEvent.click(screen.getByRole("link", { name: "Pending" }));

    expect(await screen.findByRole("heading", { name: "Pending" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/pending");
    expect(router.state.location.pathname).toBe("/pending");
  });

  // Guards can have side effects on app state, so hovering a link must not
  // run the target's beforeLoad ahead of a click.
  it("does not preload a link's route on hover", async () => {
    const onPendingLoad = vi.fn();
    await renderAt("/", { authenticated: true, pendingPath: null }, onPendingLoad);
    await screen.findByRole("heading", { name: "Home" });

    await userEvent.hover(screen.getByRole("link", { name: "Pending" }));
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(onPendingLoad).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("link", { name: "Pending" }));
    await screen.findByRole("heading", { name: "Pending" });
    expect(onPendingLoad).toHaveBeenCalled();
  });
});

describe("guards", () => {
  it("carries the guarded location, search included, to the login page as its return path", async () => {
    const router = await renderAt("/items/42?view=full", { authenticated: false, pendingPath: null });
    expect(await screen.findByRole("heading", { name: "Login" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
    expect(router.state.location.search).toEqual({ redirect: "/items/42?view=full" });
    expect(router.history.length).toBe(1);
  });

  it("sends an anonymous visitor to the login page, replacing the entry", async () => {
    const router = await renderAt("/", { authenticated: false, pendingPath: null });
    expect(await screen.findByRole("heading", { name: "Login" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
    expect(router.history.length).toBe(1);
  });

  it("redirects a signed-in visitor on to another page", async () => {
    const router = await renderAt("/", { authenticated: true, pendingPath: "/pending" });
    expect(await screen.findByRole("heading", { name: "Pending" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/pending");
  });

  it("re-runs on invalidate with new context", async () => {
    const context: TestContext = { authenticated: false, pendingPath: null };
    const router = await renderAt("/", context);
    await screen.findByRole("heading", { name: "Login" });

    await router.navigate({ href: "/" });
    await screen.findByRole("heading", { name: "Login" });

    router.update({ ...router.options, context: { authenticated: true, pendingPath: null } });
    await router.navigate({ href: "/" });
    expect(await screen.findByRole("heading", { name: "Home" })).toBeInTheDocument();
  });
});

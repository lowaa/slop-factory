import type { ReactNode } from "react";
import { render, type RenderResult } from "@testing-library/react";
import { createTestRouter, type TestRouter } from "./test_router";
import { RenderUi, RouterWithUi } from "./test_router_provider";

export interface RenderWithRouterOptions {
  // Where the memory history starts, search and hash included.
  initialPath?: string;
}

export type RenderWithRouterResult = RenderResult & {
  router: TestRouter;
};

// renderWithRouter renders a component that uses the router (a Link, a
// navigate hook) inside a router of its own with memory history, so a test
// needs neither the app's route tree nor the real address bar. Assert on
// result.router.state.location for where the component navigated to.
export async function renderWithRouter(
  ui: ReactNode,
  { initialPath = "/" }: RenderWithRouterOptions = {},
): Promise<RenderWithRouterResult> {
  const router = createTestRouter(initialPath, RenderUi);
  // Loaded before rendering, so the first render already shows ui rather
  // than an empty router still resolving its first match.
  await router.load();
  const result = render(<RouterWithUi router={router}>{ui}</RouterWithUi>);
  return {
    ...result,
    // Keeps the router around ui on a rerender, as render's own does not.
    rerender: (next: ReactNode) => result.rerender(<RouterWithUi router={router}>{next}</RouterWithUi>),
    router,
  };
}

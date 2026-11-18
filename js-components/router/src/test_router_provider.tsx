import { useContext, useState, type ReactNode } from "react";
import { RouterProvider } from "@tanstack/react-router";
import { UiContext, createTestRouter, type TestRouter } from "./test_router";

export function RenderUi() {
  return <>{useContext(UiContext)}</>;
}

export function RouterWithUi({ router, children }: { router: TestRouter; children: ReactNode }) {
  return (
    <UiContext.Provider value={children}>
      <RouterProvider router={router} />
    </UiContext.Provider>
  );
}

// TestRouterProvider is renderWithRouter's router as a component, for
// where there is no render call to wrap: a Storybook decorator, say.
export function TestRouterProvider({ children, initialPath = "/" }: { children: ReactNode; initialPath?: string }) {
  // Memory history holds nothing outside the router, so there is nothing
  // to tear down on unmount (unlike a browser history's window listeners).
  const [router] = useState(() => createTestRouter(initialPath, RenderUi));
  return <RouterWithUi router={router}>{children}</RouterWithUi>;
}

import { useCallback, useMemo } from "react";
import { useRouter } from "@tanstack/react-router";

// The part of @slop-factory/auth's AuthPagesConfig that is about
// navigation. Declared here structurally rather than imported, so this
// package does not depend on the auth package -- an app spreads the result
// straight into its AuthPagesConfig.
export interface AuthNavigation<TRoutes> {
  navigate: (path: string) => void;
  routes: TRoutes;
}

// useAuthNavigation adapts the router to the auth pages: their links and
// post-action moves (register -> login, say) become in-app navigations,
// pushed onto history like a clicked link. It must be an in-app navigation
// rather than a page load -- an app may hold state across a round trip
// through its login page that a full load would lose.
//
// routes should be a stable reference (a module constant) -- the result is
// memoized on it, so an auth provider's context value does not change on
// every render.
export function useAuthNavigation<TRoutes>(routes: TRoutes): AuthNavigation<TRoutes> {
  const navigate = useNavigateToPath();
  return useMemo(() => ({ navigate, routes }), [navigate, routes]);
}

// useNavigateToPath returns a navigate(path) for code that holds a plain
// path string rather than a typed route: a component shared across apps,
// or one handed a path to go to.
export function useNavigateToPath(): (path: string) => void {
  const router = useRouter();
  return useCallback(
    (path: string) => {
      void router.navigate({ href: path });
    },
    [router],
  );
}
